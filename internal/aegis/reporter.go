package aegis

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"net/http"
	"sync"
	"time"

	v1 "github.com/garethgeorge/backrest/gen/go/v1"
	"github.com/garethgeorge/backrest/internal/config"
	"github.com/garethgeorge/backrest/internal/oplog"
	"go.uber.org/zap"
)

const (
	// scanOverlap rescans recent changes: two writers can commit their
	// modification numbers out of order, and the ledger makes rescans free.
	scanOverlap = 500
	// wakeDelay gathers a burst of operation changes (a running backup
	// reports progress every few seconds) into one scan.
	wakeDelay = 2 * time.Second
	// scanInterval catches anything a wake-up missed.
	scanInterval = 30 * time.Second
	// batchBytes keeps a report well under Aegis Cloud's 1 MB limit.
	batchBytes    = 500 * 1024
	minBatchBytes = 16 * 1024
	// sendSpacing keeps a backlog under Aegis Cloud's 120 reports a minute.
	sendSpacing = time.Second
	minBackoff  = 5 * time.Second
	maxBackoff  = 10 * time.Minute
)

// Reporting states shown in the Aegis Agent interface.
const (
	StateDisabled   = "disabled"
	StateConnecting = "connecting"
	StateConnected  = "connected"
	StateRetrying   = "retrying"
	StateStopped    = "stopped"
)

// Status is what the interface shows about the connection to Aegis Cloud.
type Status struct {
	Enabled       bool               `json:"enabled"`
	State         string             `json:"state"`
	ReportURL     string             `json:"reportUrl,omitempty"`
	LastSuccessAt string             `json:"lastSuccessAt,omitempty"`
	LastAttemptAt string             `json:"lastAttemptAt,omitempty"`
	NextAttemptAt string             `json:"nextAttemptAt,omitempty"`
	LastError     string             `json:"lastError,omitempty"`
	Pending       int                `json:"pending"`
	InstanceID    string             `json:"instanceId,omitempty"`
	Repositories  []RepositoryPreset `json:"repositories"`
	// Runtime is docker, systemd or other; the interface uses it to suggest
	// a restore target the Docker image can write (/restores).
	Runtime string `json:"runtime"`
}

// Reporter reports this agent's operations to Aegis Cloud. It never blocks
// the orchestrator: the operation log only wakes it, and all work happens on
// its own goroutine from a durable outbox.
type Reporter struct {
	cfg       Config
	configMgr *config.ConfigManager
	ops       *oplog.OpLog
	logs      logReader
	outbox    *outbox
	client    *client
	installID string
	wake      chan struct{}

	mu            sync.Mutex
	status        Status
	backoff       time.Duration
	batchBytes    int
	lastInventory time.Time
	inventoryDue  bool
}

// NewReporter prepares reporting; Run starts it. When the environment does
// not configure Aegis Cloud, the reporter only serves its status.
func NewReporter(cfg Config, configMgr *config.ConfigManager, ops *oplog.OpLog, logs logReader, db *sql.DB) (*Reporter, error) {
	r := &Reporter{
		cfg:          cfg,
		configMgr:    configMgr,
		ops:          ops,
		logs:         logs,
		wake:         make(chan struct{}, 1),
		batchBytes:   batchBytes,
		inventoryDue: true,
		status: Status{
			Enabled:      cfg.Enabled(),
			State:        StateDisabled,
			ReportURL:    cfg.ReportURL,
			InstanceID:   cfg.InstanceID,
			Repositories: cfg.Repositories,
			Runtime:      detectRuntime(cfg.Runtime),
		},
	}
	if r.status.Repositories == nil {
		r.status.Repositories = []RepositoryPreset{}
	}
	if !cfg.Enabled() {
		return r, nil
	}
	box, err := newOutbox(db)
	if err != nil {
		return nil, err
	}
	installID, err := box.installID()
	if err != nil {
		return nil, fmt.Errorf("aegis install id: %w", err)
	}
	r.outbox = box
	r.installID = installID
	r.client = newClient(cfg.ReportURL, cfg.ReportToken, cfg.AgentVersion)
	r.status.State = StateConnecting
	return r, nil
}

func (r *Reporter) signal() {
	select {
	case r.wake <- struct{}{}:
	default:
	}
}

// Run reports until ctx ends.
func (r *Reporter) Run(ctx context.Context) {
	if !r.cfg.Enabled() {
		zap.L().Info("Aegis Cloud reporting is not configured")
		return
	}
	zap.L().Info("reporting to Aegis Cloud", zap.String("url", r.cfg.ReportURL), zap.String("install_id", r.installID))

	subscription := oplog.Subscription(func(_ []*v1.Operation, _ oplog.OperationEvent) { r.signal() })
	r.ops.Subscribe(oplog.Query{}, &subscription)
	defer func() { _ = r.ops.Unsubscribe(&subscription) }()
	configChanges := r.configMgr.OnChange.Subscribe()
	defer r.configMgr.OnChange.Unsubscribe(configChanges)

	heartbeat := time.NewTicker(r.cfg.HeartbeatInterval)
	defer heartbeat.Stop()

	var retryAt time.Time
	forceReport := true // report once at start: the server is up
	for {
		if err := r.scan(ctx); err != nil && ctx.Err() == nil {
			zap.L().Warn("aegis: scanning operations failed", zap.Error(err))
		}
		if r.stopped() {
			<-ctx.Done()
			return
		}
		if !time.Now().Before(retryAt) {
			if delay, err := r.flush(ctx, forceReport); err != nil {
				retryAt = time.Now().Add(delay)
			} else {
				retryAt = time.Time{}
				forceReport = false
			}
		}

		wait := scanInterval
		if !retryAt.IsZero() {
			wait = min(wait, time.Until(retryAt))
		}
		timer := time.NewTimer(max(wait, 0))
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-r.wake:
			// Let a burst of changes settle into one scan.
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-time.After(wakeDelay):
			}
			select {
			case <-r.wake:
			default:
			}
		case <-heartbeat.C:
			forceReport = true
		case <-configChanges:
			r.mu.Lock()
			r.inventoryDue = true
			r.mu.Unlock()
			forceReport = true
		case <-timer.C:
		}
		timer.Stop()
	}
}

// scan records the events of operations changed since the last scan.
func (r *Reporter) scan(ctx context.Context) error {
	cfg, err := r.configMgr.Get()
	if err != nil {
		return err
	}
	if cfg.Instance == "" {
		return nil // not set up yet: no operation can exist
	}
	repos := aegisRepos(cfg)
	watermark, err := r.outbox.watermark()
	if err != nil {
		return err
	}
	highest := watermark
	var events []pendingEvent
	query := oplog.Query{}.
		SetInstanceID(cfg.Instance).
		SetOriginalInstanceKeyid("").
		SetModnoGte(max(watermark-scanOverlap, 0))
	if err := r.ops.Query(query, func(op *v1.Operation) error {
		highest = max(highest, op.Modno)
		if _, ok := repos[op.RepoId]; !ok {
			return nil // not on an Aegis Cloud storage: stays private
		}
		for _, phase := range phasesOf(op) {
			events = append(events, pendingEvent{Key: eventKey(r.installID, op.Id, phase), OpID: op.Id, Phase: phase})
		}
		return nil
	}); err != nil {
		return err
	}
	now := time.Now()
	added, err := r.outbox.record(ctx, events, highest, now)
	if err != nil {
		return err
	}
	if added > 0 {
		// Snapshot counts and sizes change with every run: send them along.
		r.mu.Lock()
		r.inventoryDue = true
		r.mu.Unlock()
	}
	if dropped, err := r.outbox.trim(now); err != nil {
		return err
	} else if dropped > 0 {
		zap.L().Warn("aegis: outbox full, dropped the oldest events", zap.Int64("dropped", dropped))
	}
	return nil
}

func (r *Reporter) agentInfo(cfg *v1.Config) AgentInfo {
	sys := readSystemInfo(r.cfg.Runtime)
	return AgentInfo{
		InstallID:     r.installID,
		InstanceID:    clamp(cfg.GetInstance(), 64),
		Hostname:      clamp(sys.Hostname, 255),
		OS:            clamp(sys.OS, 255),
		Arch:          clamp(sys.Arch, 32),
		AgentVersion:  clamp(r.cfg.AgentVersion, 64),
		ResticVersion: clamp(r.cfg.ResticVersion, 64),
		Runtime:       sys.Runtime,
	}
}

// eventsFor builds the pending events; the ones that no longer exist (the
// operation was deleted, or its repository removed) are returned as gone.
func (r *Reporter) eventsFor(cfg *v1.Config, pending []pendingEvent) (events []Event, gone []string) {
	repos := aegisRepos(cfg)
	byGUID := make(map[string]RepoRef)
	for _, repo := range repos {
		if repo.GUID != "" {
			byGUID[repo.GUID] = repo
		}
	}
	now := time.Now()
	for _, item := range pending {
		op, err := r.ops.Get(item.OpID)
		if err != nil {
			gone = append(gone, item.Key)
			continue
		}
		repo, ok := repos[op.RepoId]
		if !ok {
			repo, ok = byGUID[op.RepoGuid]
		}
		if !ok {
			gone = append(gone, item.Key)
			continue
		}
		event, ok := buildEvent(op, item.Phase, r.installID, repo, r.logs, now)
		if !ok {
			gone = append(gone, item.Key)
			continue
		}
		events = append(events, event)
	}
	return events, gone
}

// flush sends the pending events (and the heartbeat or inventory when due),
// batch by batch. On failure it returns how long to wait before trying again.
func (r *Reporter) flush(ctx context.Context, heartbeat bool) (time.Duration, error) {
	for first := true; ; first = false {
		cfg, err := r.configMgr.Get()
		if err != nil {
			return minBackoff, err
		}
		r.mu.Lock()
		includeInventory := first && cfg.Instance != "" &&
			(r.inventoryDue || time.Since(r.lastInventory) >= inventoryInterval)
		limit := r.batchBytes
		r.mu.Unlock()

		pending, err := r.outbox.pending(maxEventsPerReport)
		if err != nil {
			return minBackoff, err
		}
		if len(pending) == 0 && !(first && (heartbeat || includeInventory)) {
			return 0, nil
		}

		events, gone := r.eventsFor(cfg, pending)
		if err := r.outbox.markSent(gone, time.Now()); err != nil {
			return minBackoff, err
		}
		batch := fitBatch(events, limit)
		if batch == nil {
			batch = []Event{} // "events": [] on a heartbeat, never null
		}

		report := Report{
			Schema: SchemaVersion,
			SentAt: time.Now().UTC().Format(time.RFC3339Nano),
			Agent:  r.agentInfo(cfg),
			Events: batch,
		}
		if includeInventory {
			inventory, err := buildInventory(cfg, r.ops)
			if err != nil {
				zap.L().Warn("aegis: building the inventory failed", zap.Error(err))
			} else {
				report.Inventory = inventory
			}
		}

		r.noteAttempt()
		response, err := r.client.send(ctx, report)
		if err != nil {
			return r.failed(err, batch)
		}
		keys := make([]string, 0, len(batch))
		for _, event := range batch {
			keys = append(keys, event.Key)
		}
		for _, result := range response.Results {
			if result.Status == "rejected" {
				zap.L().Warn("aegis: Aegis Cloud rejected an event", zap.String("key", result.Key), zap.String("reason", result.Reason))
			}
		}
		if err := r.outbox.markSent(keys, time.Now()); err != nil {
			return minBackoff, err
		}
		r.succeeded(report.Inventory != nil)

		if len(pending) == len(gone) && len(batch) == 0 {
			continue // everything pending was gone: look again
		}
		if len(batch) < len(events) || len(pending) == maxEventsPerReport {
			// A backlog: keep draining, spaced out.
			select {
			case <-ctx.Done():
				return 0, ctx.Err()
			case <-time.After(sendSpacing):
			}
			continue
		}
		return 0, nil
	}
}

// fitBatch takes events in order while their JSON stays under limit bytes;
// always at least one, so an oversized event gets its answer (413) alone.
func fitBatch(events []Event, limit int) []Event {
	size := 0
	for i, event := range events {
		encoded, err := json.Marshal(event)
		if err != nil {
			continue
		}
		size += len(encoded) + 1
		if size > limit && i > 0 {
			return events[:i]
		}
	}
	return events
}

func (r *Reporter) noteAttempt() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.status.LastAttemptAt = time.Now().UTC().Format(time.RFC3339)
}

func (r *Reporter) succeeded(sentInventory bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.backoff = 0
	r.status.State = StateConnected
	r.status.LastSuccessAt = time.Now().UTC().Format(time.RFC3339)
	r.status.NextAttemptAt = ""
	r.status.LastError = ""
	if sentInventory {
		r.lastInventory = time.Now()
		r.inventoryDue = false
	}
}

func (r *Reporter) failed(err error, batch []Event) (time.Duration, error) {
	var cloud *cloudError
	if errors.As(err, &cloud) {
		switch {
		case cloud.stopsReporting():
			r.mu.Lock()
			r.status.State = StateStopped
			r.status.LastError = cloud.Message
			r.status.NextAttemptAt = ""
			r.mu.Unlock()
			zap.L().Error("aegis: Aegis Cloud refused this agent; reporting stopped", zap.Error(err))
			return 0, err
		case cloud.Status == http.StatusRequestEntityTooLarge:
			r.mu.Lock()
			r.batchBytes = max(r.batchBytes/2, minBatchBytes)
			r.mu.Unlock()
			if len(batch) == 1 {
				// One event is too large on its own: give it up rather than block the queue.
				zap.L().Warn("aegis: dropping an event Aegis Cloud finds too large", zap.String("key", batch[0].Key))
				_ = r.outbox.markSent([]string{batch[0].Key}, time.Now())
			}
			return 0, err
		case cloud.Status == http.StatusTooManyRequests && cloud.RetryAfter > 0:
			return r.retryIn(cloud.RetryAfter, err)
		}
	}
	r.mu.Lock()
	if r.backoff == 0 {
		r.backoff = minBackoff
	} else {
		r.backoff = min(r.backoff*2, maxBackoff)
	}
	delay := r.backoff
	r.mu.Unlock()
	// ±20% so many agents do not retry in step.
	delay = time.Duration(float64(delay) * (0.8 + 0.4*rand.Float64()))
	return r.retryIn(delay, err)
}

func (r *Reporter) retryIn(delay time.Duration, err error) (time.Duration, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.status.State = StateRetrying
	r.status.LastError = err.Error()
	r.status.NextAttemptAt = time.Now().Add(delay).UTC().Format(time.RFC3339)
	zap.L().Warn("aegis: reporting to Aegis Cloud failed, retrying", zap.Error(err), zap.Duration("in", delay))
	return delay, err
}

func (r *Reporter) stopped() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.status.State == StateStopped
}

// Status is a snapshot for the interface.
func (r *Reporter) Status() Status {
	r.mu.Lock()
	status := r.status
	r.mu.Unlock()
	if r.outbox != nil {
		if count, err := r.outbox.pendingCount(); err == nil {
			status.Pending = count
		}
	}
	return status
}

// StatusHandler serves GET /aegis/status (behind the interface login).
func (r *Reporter) StatusHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		_ = json.NewEncoder(w).Encode(r.Status())
	})
}

// ValidateConfigUpdate refuses settings Aegis Agent does not allow: the
// interface always requires a login.
func ValidateConfigUpdate(next *v1.Config) error {
	if next.GetAuth().GetDisabled() {
		return errors.New("Aegis Agent always requires a login: add a user instead of disabling authentication")
	}
	return nil
}
