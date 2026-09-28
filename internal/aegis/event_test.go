package aegis

import (
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"testing"
	"time"

	v1 "github.com/garethgeorge/backrest/gen/go/v1"
	"github.com/garethgeorge/backrest/pkg/restic"
)

// realPruneOutput is restic 0.19.1's prune output, captured from a real run.
const realPruneOutput = `loading all snapshots...
finding data that is still in use for 1 snapshots
[0:00] 100.00%  1 / 1 snapshots
searching used packs...
collecting packs for deletion and repacking
[0:00] 100.00%  4 / 4 packs processed

to repack:             6 blobs / 4.769 MiB
this removes:          3 blobs / 2.861 MiB
to delete:             2 blobs / 718 B
total prune:           5 blobs / 2.862 MiB
remaining:             6 blobs / 2.862 MiB
unused size after prune: 0 B (0.00% of remaining size)

repacking packs
[0:00] 100.00%  1 / 1 packs repacked
rebuilding index
[0:00] 100.00%  3 / 3 indexes processed
[0:00] 100.00%  3 / 3 old indexes deleted
removing 2 old packs
[0:00] 100.00%  2 / 2 files deleted
done
`

type fakeLogs map[string]string

func (l fakeLogs) Open(id string) (io.ReadCloser, error) {
	value, ok := l[id]
	if !ok {
		return nil, errors.New("no such log")
	}
	return io.NopCloser(strings.NewReader(value)), nil
}

var testRepo = RepoRef{ID: "asia", GUID: "guid", Bucket: "aegis-abc123-apac-xyz789", Prefix: "servers/agt_aaaaaaaaaaaa"}

func TestParseReclaimed(t *testing.T) {
	got := parseReclaimed(realPruneOutput)
	if got == nil || *got != 3001025 {
		t.Fatalf("reclaimed from restic's output = %v, want 3001025 (2.862 MiB)", got)
	}
	for output, want := range map[string]int64{
		"total prune:           0 blobs / 0 B\n":       0,
		"total prune:         12 blobs / 1.500 GiB\n":  1610612736,
		"total prune:          1 blobs / 718 B\r\n":    718,
		"total prune:          3 blobs / 12.250 KiB\n": 12544,
	} {
		if got := parseReclaimed(output); got == nil || *got != want {
			t.Errorf("parseReclaimed(%q) = %v, want %d", output, got, want)
		}
	}
	if got := parseReclaimed("done\n"); got != nil {
		t.Errorf("no total prune line must give nil, got %d", *got)
	}
}

func TestClampAndTailKeepCharacters(t *testing.T) {
	value := "ab€cd" // € is three bytes
	if got := clamp(value, 3); got != "ab" {
		t.Errorf("clamp = %q, want %q", got, "ab")
	}
	if got := clamp(value, 5); got != "ab€" {
		t.Errorf("clamp = %q, want %q", got, "ab€")
	}
	if got := tail(value, 4); got != "cd" {
		t.Errorf("tail = %q, want %q", got, "cd")
	}
	if got := tail(value, 5); got != "€cd" {
		t.Errorf("tail = %q, want %q", got, "€cd")
	}
}

func backupOp(status v1.OperationStatus) *v1.Operation {
	return &v1.Operation{
		Id:              42,
		FlowId:          40,
		RepoId:          "asia",
		RepoGuid:        "guid",
		PlanId:          "daily",
		InstanceId:      "web-01",
		Status:          status,
		UnixTimeStartMs: 1_790_000_000_000,
		UnixTimeEndMs:   1_790_000_120_000,
		Logref:          "task-log",
		Op: &v1.Operation_OperationBackup{OperationBackup: &v1.OperationBackup{
			LastStatus: &v1.BackupProgressEntry{Entry: &v1.BackupProgressEntry_Summary{Summary: &v1.BackupProgressSummary{
				FilesNew: 3, FilesChanged: 1, FilesUnmodified: 90, DataAdded: 500,
				TotalFilesProcessed: 94, TotalBytesProcessed: 10_000, TotalDuration: 120.5,
				SnapshotId: "1a2b3c4d",
			}}},
		}},
	}
}

func TestBuildBackupEvents(t *testing.T) {
	now := time.UnixMilli(1_790_000_200_000)

	running := backupOp(v1.OperationStatus_STATUS_INPROGRESS)
	if phases := phasesOf(running); len(phases) != 1 || phases[0] != phaseStarted {
		t.Fatalf("a running backup produces %v, want [started]", phases)
	}
	started, ok := buildEvent(running, phaseStarted, "install", testRepo, nil, now)
	if !ok || started.Key != "install:42:started" || started.Status != "" || started.FinishedAt != "" {
		t.Fatalf("started event = %+v", started)
	}

	success := backupOp(v1.OperationStatus_STATUS_SUCCESS)
	event, ok := buildEvent(success, phaseFinished, "install", testRepo, nil, now)
	if !ok {
		t.Fatal("a successful backup produces a finished event")
	}
	if event.Key != "install:42:finished" || event.Status != "success" || event.PlanID != "daily" ||
		event.SnapshotID != "1a2b3c4d" || event.Stats == nil || event.Stats.BytesProcessed != 10_000 ||
		event.StartedAt != "2026-09-21T14:13:20Z" || event.FinishedAt != "2026-09-21T14:15:20Z" {
		t.Fatalf("finished event = %+v", event)
	}
	if event.Message != "" || event.Detail != "" {
		t.Errorf("a success carries no message or detail: %+v", event)
	}

	// Warnings list the files restic could not read, at most 20.
	warning := backupOp(v1.OperationStatus_STATUS_WARNING)
	warning.DisplayMessage = "Some files could not be read"
	for i := 0; i < 25; i++ {
		warning.GetOperationBackup().Errors = append(warning.GetOperationBackup().Errors,
			&v1.BackupProgressError{Item: "/srv/locked.db", During: "archival", Message: "permission denied"})
	}
	event, _ = buildEvent(warning, phaseFinished, "install", testRepo, fakeLogs{"task-log": strings.Repeat("x", 10_000) + "END"}, now)
	if event.Status != "warning" || len(event.FileErrors) != 20 || event.FileErrors[0].Item != "/srv/locked.db" {
		t.Fatalf("warning event = %+v", event)
	}
	if len(event.Detail) != maxDetailBytes || !strings.HasSuffix(event.Detail, "END") {
		t.Errorf("detail keeps the last 8 KB of the log, got %d bytes", len(event.Detail))
	}

	failed := backupOp(v1.OperationStatus_STATUS_ERROR)
	failed.DisplayMessage = "Fatal: unable to open config file"
	failed.UnixTimeEndMs = 0
	event, _ = buildEvent(failed, phaseFinished, "install", testRepo, nil, now)
	if event.Status != "failed" || event.Message != "Fatal: unable to open config file" ||
		event.FinishedAt != "2026-09-21T14:16:40Z" {
		t.Fatalf("failed event without an end time finishes now: %+v", event)
	}

	cancelled := backupOp(v1.OperationStatus_STATUS_USER_CANCELLED)
	event, _ = buildEvent(cancelled, phaseFinished, "install", testRepo, nil, now)
	if event.Status != "failed" || event.Message != "Cancelled by the user." {
		t.Fatalf("cancelled event = %+v", event)
	}

	dryRun := backupOp(v1.OperationStatus_STATUS_SUCCESS)
	dryRun.GetOperationBackup().DryRun = true
	if _, ok := buildEvent(dryRun, phaseFinished, "install", testRepo, nil, now); ok {
		t.Error("dry runs are not reported")
	}
	if phases := phasesOf(backupOp(v1.OperationStatus_STATUS_PENDING)); len(phases) != 0 {
		t.Errorf("pending backups produce nothing, got %v", phases)
	}
}

func TestBuildMaintenanceEvents(t *testing.T) {
	now := time.UnixMilli(1_790_000_200_000)
	prune := &v1.Operation{
		Id: 7, FlowId: 7, RepoId: "asia", RepoGuid: "guid", PlanId: "_system_", InstanceId: "web-01",
		Status: v1.OperationStatus_STATUS_SUCCESS, UnixTimeStartMs: 1_790_000_000_000, UnixTimeEndMs: 1_790_000_010_000,
		Op: &v1.Operation_OperationPrune{OperationPrune: &v1.OperationPrune{OutputLogref: "prune-log"}},
	}
	event, ok := buildEvent(prune, phaseFinished, "install", testRepo, fakeLogs{"prune-log": realPruneOutput}, now)
	if !ok || event.Kind != "prune" || event.PlanID != "_system_" || event.BytesReclaimed == nil || *event.BytesReclaimed != 3001025 {
		t.Fatalf("prune event = %+v", event)
	}
	if phases := phasesOf(&v1.Operation{Status: v1.OperationStatus_STATUS_INPROGRESS, Op: prune.Op}); len(phases) != 0 {
		t.Errorf("a running prune produces nothing until it finishes, got %v", phases)
	}

	check := &v1.Operation{
		Id: 8, FlowId: 8, RepoId: "asia", RepoGuid: "guid", PlanId: "_system_", InstanceId: "web-01",
		Status: v1.OperationStatus_STATUS_ERROR, DisplayMessage: "check: repository contains errors",
		UnixTimeStartMs: 1_790_000_000_000, UnixTimeEndMs: 1_790_000_010_000,
		Op: &v1.Operation_OperationCheck{OperationCheck: &v1.OperationCheck{OutputLogref: "check-log"}},
	}
	event, _ = buildEvent(check, phaseFinished, "install", testRepo, fakeLogs{"check-log": "pack abc: not referenced\nFatal: repository contains errors"}, now)
	if event.Kind != "check" || event.Status != "failed" || !strings.Contains(event.Detail, "repository contains errors") {
		t.Fatalf("check event = %+v", event)
	}

	forget := &v1.Operation{
		Id: 9, FlowId: 9, RepoId: "asia", RepoGuid: "guid", PlanId: "daily", InstanceId: "web-01",
		Status: v1.OperationStatus_STATUS_SUCCESS, UnixTimeStartMs: 1_790_000_000_000, UnixTimeEndMs: 1_790_000_001_000,
		Op: &v1.Operation_OperationForget{OperationForget: &v1.OperationForget{}},
	}
	if event, ok := buildEvent(forget, phaseFinished, "install", testRepo, nil, now); !ok || event.Kind != "forget" {
		t.Fatalf("forget event = %+v", event)
	}

	stats := &v1.Operation{Status: v1.OperationStatus_STATUS_SUCCESS, Op: &v1.Operation_OperationStats{}}
	if _, ok := buildEvent(stats, phaseFinished, "install", testRepo, nil, now); ok {
		t.Error("stats runs are not reported")
	}
}

func TestCommandLinesAreNotReported(t *testing.T) {
	now := time.UnixMilli(1_790_000_200_000)
	// Written exactly as pkg/restic writes them: the command line in the
	// operation log, and the command quoted (cut to 100 bytes) in the error.
	cmd := exec.Command("/bin/restic", "backup", "--json",
		"-r", "s3:https://0123456789abcdef0123456789abcdef.r2.cloudflarestorage.com/aegis-abc123-apac-xyz789/servers/agt_x",
		"--password-command", "cat /run/secrets/restic", "-o", "s3.region=auto", "/srv")
	logLine := fmt.Sprintf("command: %q\n", cmd)
	cmdError := &restic.CmdError{Command: cmd.String()[:100] + "...", Err: errors.New("exit status 1")}

	failed := backupOp(v1.OperationStatus_STATUS_ERROR)
	failed.DisplayMessage = `backup for plan "daily": ` + cmdError.Error() + "\nOutput:\nFatal: unable to open repository"
	event, _ := buildEvent(failed, phaseFinished, "install", testRepo,
		fakeLogs{"task-log": logLine + "Fatal: unable to open repository\n"}, now)

	for _, text := range []string{event.Message, event.Detail} {
		for _, secret := range []string{"/bin/restic", "--json", "password-command", "s3.region"} {
			if strings.Contains(text, secret) {
				t.Errorf("reported %q, which holds the command line: %q", secret, text)
			}
		}
	}
	if want := "backup for plan \"daily\": restic failed: exit status 1\nOutput:\nFatal: unable to open repository"; event.Message != want {
		t.Errorf("message = %q, want %q", event.Message, want)
	}
	if want := "Fatal: unable to open repository\n"; event.Detail != want {
		t.Errorf("detail = %q, want %q", event.Detail, want)
	}
}

func TestFieldsFitCloudLimits(t *testing.T) {
	op := backupOp(v1.OperationStatus_STATUS_ERROR)
	op.PlanId = strings.Repeat("p", 150)
	op.DisplayMessage = strings.Repeat("m", 5000)
	op.GetOperationBackup().Errors = []*v1.BackupProgressError{{Item: strings.Repeat("i", 5000), Message: strings.Repeat("e", 2000)}}
	event, _ := buildEvent(op, phaseFinished, "install", testRepo, nil, time.Now())
	if len(event.PlanID) != 100 || len(event.Message) != maxMessageBytes ||
		len(event.FileErrors[0].Item) != 4096 || len(event.FileErrors[0].Message) != 1000 {
		t.Fatalf("fields must be cut to Aegis Cloud's limits: plan %d, message %d, item %d, error %d",
			len(event.PlanID), len(event.Message), len(event.FileErrors[0].Item), len(event.FileErrors[0].Message))
	}
}
