package aegis

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	v1 "github.com/garethgeorge/backrest/gen/go/v1"
	"github.com/garethgeorge/backrest/internal/config"
	"github.com/garethgeorge/backrest/internal/config/migrations"
	"github.com/garethgeorge/backrest/internal/kvstore"
	"github.com/garethgeorge/backrest/internal/oplog"
	"github.com/garethgeorge/backrest/internal/oplog/sqlitestore"
)

// fakeCloud records the reports it receives and answers like Aegis Cloud.
type fakeCloud struct {
	mu      sync.Mutex
	reports []Report
	status  int // answer with this status instead of 200 when set
	headers map[string]string
}

func (c *fakeCloud) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Authorization") != "Bearer aegis_rt_test" || r.Header.Get("Content-Encoding") != "gzip" {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	reader, err := gzip.NewReader(r.Body)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	body, _ := io.ReadAll(reader)
	// Aegis Cloud validates the envelope strictly: events must be an array.
	if !strings.Contains(string(body), `"events":[`) {
		w.WriteHeader(http.StatusUnprocessableEntity)
		return
	}
	var report Report
	if err := json.Unmarshal(body, &report); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.reports = append(c.reports, report)
	for key, value := range c.headers {
		w.Header().Set(key, value)
	}
	if c.status != 0 {
		w.WriteHeader(c.status)
		_, _ = w.Write([]byte(`{"error":{"code":"token_revoked","message":"This report token was replaced."}}`))
		return
	}
	results := make([]EventResult, 0, len(report.Events))
	for _, event := range report.Events {
		results = append(results, EventResult{Key: event.Key, Status: "accepted"})
	}
	_ = json.NewEncoder(w).Encode(reportResponse{Results: results, ServerTime: time.Now().UTC().Format(time.RFC3339)})
}

func (c *fakeCloud) received() []Report {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]Report(nil), c.reports...)
}

func (c *fakeCloud) answer(status int, headers map[string]string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.status = status
	c.headers = headers
}

const otherURI = "/srv/local-repo"

// Repository ids restic generates: 64 hexadecimal characters.
var (
	guidAsia  = strings.Repeat("a1", 32)
	guidLocal = strings.Repeat("b2", 32)
)

func testConfig() *v1.Config {
	return &v1.Config{
		Version:  migrations.CurrentVersion,
		Instance: "web-01",
		Repos: []*v1.Repo{
			{Id: "asia", Uri: testURI, Guid: guidAsia, Password: "secret"},
			{Id: "local", Uri: otherURI, Guid: guidLocal, Password: "secret"},
		},
		Plans: []*v1.Plan{
			{Id: "daily", Repo: "asia", Paths: []string{"/srv"}, Schedule: &v1.Schedule{Schedule: &v1.Schedule_Cron{Cron: "0 2 * * *"}}},
			{Id: "local-daily", Repo: "local", Paths: []string{"/home"}, Schedule: &v1.Schedule{Schedule: &v1.Schedule_Disabled{Disabled: true}}},
		},
		Auth: &v1.Auth{Disabled: true},
	}
}

type harness struct {
	reporter *Reporter
	ops      *oplog.OpLog
	cloud    *fakeCloud
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	cloud := &fakeCloud{}
	server := httptest.NewServer(cloud)
	t.Cleanup(server.Close)

	store, err := sqlitestore.NewMemorySqliteStore(t)
	if err != nil {
		t.Fatal(err)
	}
	ops, err := oplog.NewOpLog(store)
	if err != nil {
		t.Fatal(err)
	}
	configMgr := &config.ConfigManager{Store: &config.MemoryStore{Config: testConfig()}}
	reporter, err := NewReporter(Config{
		ReportURL:         server.URL + "/v1/agent/reports",
		ReportToken:       "aegis_rt_test",
		HeartbeatInterval: time.Minute,
		AgentVersion:      "1.0.0",
		ResticVersion:     "0.19.1",
		Runtime:           "other",
	}, configMgr, ops, fakeLogs{}, kvstore.NewInMemorySqliteDbForKvStore(t))
	if err != nil {
		t.Fatal(err)
	}
	return &harness{reporter: reporter, ops: ops, cloud: cloud}
}

func (h *harness) cycle(t *testing.T, heartbeat bool) error {
	t.Helper()
	if err := h.reporter.scan(context.Background()); err != nil {
		t.Fatal(err)
	}
	_, err := h.reporter.flush(context.Background(), heartbeat)
	return err
}

func addBackup(t *testing.T, ops *oplog.OpLog, repoID, repoGUID, instance, keyid string) *v1.Operation {
	t.Helper()
	op := backupOp(v1.OperationStatus_STATUS_INPROGRESS)
	op.Id, op.FlowId, op.Modno = 0, 0, 0
	op.RepoId, op.RepoGuid, op.InstanceId, op.OriginalInstanceKeyid = repoID, repoGUID, instance, keyid
	op.GetOperationBackup().GetLastStatus().GetSummary().SnapshotId = ""
	if err := ops.Add(op); err != nil {
		t.Fatal(err)
	}
	return op
}

func TestReporterSendsEachEventOnce(t *testing.T) {
	h := newHarness(t)
	op := addBackup(t, h.ops, "asia", guidAsia, "web-01", "")

	if err := h.cycle(t, true); err != nil {
		t.Fatal(err)
	}
	reports := h.cloud.received()
	if len(reports) != 1 {
		t.Fatalf("first cycle sends one report, got %d", len(reports))
	}
	first := reports[0]
	if first.Schema != SchemaVersion || first.Agent.InstanceID != "web-01" || first.Agent.AgentVersion != "1.0.0" ||
		len(first.Agent.InstallID) != 36 || first.Inventory == nil {
		t.Fatalf("first report = %+v", first)
	}
	if len(first.Events) != 1 || first.Events[0].Phase != phaseStarted || first.Events[0].Repo.Bucket != "aegis-abc123-apac-xyz789" {
		t.Fatalf("a running backup is reported as started: %+v", first.Events)
	}
	// Only the Aegis repository and its plan are in the inventory.
	if len(first.Inventory.Repos) != 1 || len(first.Inventory.Plans) != 1 || first.Inventory.Plans[0].ID != "daily" ||
		first.Inventory.Plans[0].Schedule.Kind != "cron" || first.Inventory.Plans[0].Schedule.Clock != "local" {
		t.Fatalf("inventory = %+v", first.Inventory)
	}

	op.Status = v1.OperationStatus_STATUS_SUCCESS
	if err := h.ops.Update(op); err != nil {
		t.Fatal(err)
	}
	if err := h.cycle(t, false); err != nil {
		t.Fatal(err)
	}
	reports = h.cloud.received()
	if len(reports) != 2 || len(reports[1].Events) != 1 || reports[1].Events[0].Phase != phaseFinished ||
		reports[1].Events[0].Status != "success" || reports[1].Inventory == nil {
		t.Fatalf("the finished backup follows, with a fresh inventory: %+v", reports[1:])
	}

	// Rescans overlap the last changes; the ledger keeps them from being sent again.
	for range 3 {
		if err := h.cycle(t, false); err != nil {
			t.Fatal(err)
		}
	}
	if got := len(h.cloud.received()); got != 2 {
		t.Fatalf("nothing is sent twice, got %d reports", got)
	}
	if status := h.reporter.Status(); status.State != StateConnected || status.Pending != 0 || status.LastSuccessAt == "" {
		t.Fatalf("status = %+v", status)
	}
}

func TestReporterKeepsOtherRepositoriesPrivate(t *testing.T) {
	h := newHarness(t)
	addBackup(t, h.ops, "local", guidLocal, "web-01", "")         // not on Aegis Cloud
	addBackup(t, h.ops, "asia", guidAsia, "web-01", "remote-key") // synced from a peer
	addBackup(t, h.ops, "asia", guidAsia, "other-host", "")       // another instance
	if err := h.cycle(t, true); err != nil {
		t.Fatal(err)
	}
	reports := h.cloud.received()
	if len(reports) != 1 || len(reports[0].Events) != 0 {
		t.Fatalf("only the heartbeat is sent: %+v", reports)
	}
}

func TestReporterStopsWhenRefused(t *testing.T) {
	h := newHarness(t)
	addBackup(t, h.ops, "asia", guidAsia, "web-01", "")
	h.cloud.answer(http.StatusUnauthorized, nil)
	if err := h.cycle(t, true); err == nil {
		t.Fatal("a refused token is an error")
	}
	status := h.reporter.Status()
	if status.State != StateStopped || status.LastError != "This report token was replaced." || status.Pending != 1 {
		t.Fatalf("status = %+v", status)
	}
}

func TestReporterBacksOffAndHonoursRetryAfter(t *testing.T) {
	h := newHarness(t)
	addBackup(t, h.ops, "asia", guidAsia, "web-01", "")

	h.cloud.answer(http.StatusServiceUnavailable, nil)
	if err := h.reporter.scan(context.Background()); err != nil {
		t.Fatal(err)
	}
	delay, err := h.reporter.flush(context.Background(), true)
	if err == nil || delay < 4*time.Second || delay > 6*time.Second {
		t.Fatalf("first retry in about 5s, got %v (%v)", delay, err)
	}
	delay, _ = h.reporter.flush(context.Background(), true)
	if delay < 8*time.Second || delay > 12*time.Second {
		t.Fatalf("second retry in about 10s, got %v", delay)
	}
	if status := h.reporter.Status(); status.State != StateRetrying || status.NextAttemptAt == "" {
		t.Fatalf("status = %+v", status)
	}

	h.cloud.answer(http.StatusTooManyRequests, map[string]string{"Retry-After": "42"})
	if delay, _ := h.reporter.flush(context.Background(), true); delay != 42*time.Second {
		t.Fatalf("Retry-After is honoured, got %v", delay)
	}

	h.cloud.answer(http.StatusRequestEntityTooLarge, nil)
	_, _ = h.reporter.flush(context.Background(), true)
	if h.reporter.batchBytes != batchBytes/2 {
		t.Fatalf("a report that is too large halves the batch, got %d", h.reporter.batchBytes)
	}
	// The single event in the batch was too large alone: it is given up.
	if status := h.reporter.Status(); status.Pending != 0 {
		t.Fatalf("pending = %d", status.Pending)
	}

	h.cloud.answer(0, nil)
	if _, err := h.reporter.flush(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	if status := h.reporter.Status(); status.State != StateConnected || h.reporter.backoff != 0 {
		t.Fatalf("a success resets the backoff: %+v", status)
	}
}

func TestReporterDisabledServesStatus(t *testing.T) {
	reporter, err := NewReporter(Config{InstanceID: "web-prod-01", Runtime: "docker"}, nil, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	reporter.Run(context.Background()) // returns at once
	recorder := httptest.NewRecorder()
	reporter.StatusHandler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/aegis/status", nil))
	var status Status
	if err := json.NewDecoder(recorder.Body).Decode(&status); err != nil {
		t.Fatal(err)
	}
	if status.Enabled || status.State != StateDisabled || status.InstanceID != "web-prod-01" || status.Repositories == nil || status.Runtime != "docker" {
		t.Fatalf("status = %+v", status)
	}
}

func TestValidateConfigUpdateRequiresLogin(t *testing.T) {
	if err := ValidateConfigUpdate(&v1.Config{Auth: &v1.Auth{Disabled: true}}); err == nil {
		t.Error("turning the login off must be refused")
	}
	if err := ValidateConfigUpdate(&v1.Config{Auth: &v1.Auth{Users: []*v1.User{{Name: "admin"}}}}); err != nil {
		t.Errorf("a login with users is fine: %v", err)
	}
}
