package aegis

import (
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	v1 "github.com/garethgeorge/backrest/gen/go/v1"
)

var updateContract = flag.Bool("update-contract", false, "rewrite testdata/report-schema1.json")

// contractReport is a report as this agent builds it, covering every kind of
// event and schedule. testdata/report-schema1.json holds it; Aegis Cloud's
// tests parse that file with its own schemas, so a change on either side that
// breaks the other fails in both repositories' tests.
func contractReport() Report {
	now := time.UnixMilli(1_790_000_200_000)
	const installID = "3f2b8c1e-4a5d-4e6f-8a7b-9c0d1e2f3a4b"
	build := func(op *v1.Operation, phase string, logs logReader) Event {
		event, ok := buildEvent(op, phase, installID, testRepo, logs, now)
		if !ok {
			panic("contract event not built")
		}
		return event
	}

	running := backupOp(v1.OperationStatus_STATUS_INPROGRESS)
	success := backupOp(v1.OperationStatus_STATUS_SUCCESS)
	warning := backupOp(v1.OperationStatus_STATUS_WARNING)
	warning.Id, warning.DisplayMessage = 43, "Some files could not be read"
	warning.GetOperationBackup().Errors = []*v1.BackupProgressError{
		{Item: "/srv/app/locked.db", During: "archival", Message: "open /srv/app/locked.db: permission denied"},
	}
	failed := backupOp(v1.OperationStatus_STATUS_ERROR)
	failed.Id, failed.DisplayMessage = 44, "Fatal: unable to open config file: Stat: 403 Forbidden"
	failed.GetOperationBackup().LastStatus = nil
	prune := &v1.Operation{
		Id: 45, FlowId: 45, RepoId: "asia", RepoGuid: "guid", PlanId: "_system_", InstanceId: "web-01",
		Status: v1.OperationStatus_STATUS_SUCCESS, UnixTimeStartMs: 1_790_000_000_000, UnixTimeEndMs: 1_790_000_030_000,
		Op: &v1.Operation_OperationPrune{OperationPrune: &v1.OperationPrune{OutputLogref: "prune"}},
	}
	check := &v1.Operation{
		Id: 46, FlowId: 46, RepoId: "asia", RepoGuid: "guid", PlanId: "_system_", InstanceId: "web-01",
		Status: v1.OperationStatus_STATUS_ERROR, DisplayMessage: "check: repository contains errors",
		UnixTimeStartMs: 1_790_000_000_000, UnixTimeEndMs: 1_790_000_060_000,
		Op: &v1.Operation_OperationCheck{OperationCheck: &v1.OperationCheck{OutputLogref: "check"}},
	}
	forget := &v1.Operation{
		Id: 47, FlowId: 42, RepoId: "asia", RepoGuid: "guid", PlanId: "daily", InstanceId: "web-01",
		Status: v1.OperationStatus_STATUS_SUCCESS, UnixTimeStartMs: 1_790_000_130_000, UnixTimeEndMs: 1_790_000_131_000,
		Op: &v1.Operation_OperationForget{OperationForget: &v1.OperationForget{}},
	}
	logs := fakeLogs{"prune": realPruneOutput, "check": "Fatal: repository contains errors", "task-log": "restic output"}

	totalSize := int64(123_456_789)
	count := int64(12)
	lastBytes := int64(10_000)
	return Report{
		Schema: SchemaVersion,
		SentAt: now.UTC().Format(time.RFC3339Nano),
		Agent: AgentInfo{
			InstallID: installID, InstanceID: "web-01", Hostname: "web-prod-01", OS: "Ubuntu 24.04.1 LTS",
			Arch: "amd64", AgentVersion: "1.0.0", ResticVersion: "0.19.1", Runtime: "docker",
		},
		Events: []Event{
			build(running, phaseStarted, nil),
			build(success, phaseFinished, logs),
			build(warning, phaseFinished, logs),
			build(failed, phaseFinished, logs),
			build(prune, phaseFinished, logs),
			build(check, phaseFinished, logs),
			build(forget, phaseFinished, logs),
		},
		Inventory: &Inventory{
			Repos: []InventoryRepo{{RepoRef: testRepo, Stats: &RepoStats{TotalSize: totalSize, SnapshotCount: count, At: isoMs(1_790_000_100_000)}}},
			Plans: []InventoryPlan{
				{ID: "daily", RepoID: "asia", Paths: []string{"/userdata/srv"}, Schedule: scheduleOf(&v1.Schedule{Schedule: &v1.Schedule_Cron{Cron: "0 2 * * *"}}), SnapshotCount: &count, LastSnapshotBytes: &lastBytes},
				{ID: "hourly", RepoID: "asia", Paths: []string{"/userdata/db"}, Schedule: scheduleOf(&v1.Schedule{Schedule: &v1.Schedule_MaxFrequencyHours{MaxFrequencyHours: 6}, Clock: v1.Schedule_CLOCK_UTC})},
				{ID: "weekly", RepoID: "asia", Paths: []string{"/userdata/home"}, Schedule: scheduleOf(&v1.Schedule{Schedule: &v1.Schedule_MaxFrequencyDays{MaxFrequencyDays: 7}, Clock: v1.Schedule_CLOCK_LAST_RUN_TIME})},
				{ID: "paused", RepoID: "asia", Paths: []string{"/userdata/tmp"}, Schedule: scheduleOf(&v1.Schedule{Schedule: &v1.Schedule_Disabled{Disabled: true}})},
				{ID: "zero-days", RepoID: "asia", Paths: []string{"/userdata/odd"}, Schedule: scheduleOf(&v1.Schedule{Schedule: &v1.Schedule_MaxFrequencyDays{MaxFrequencyDays: 0}})},
			},
		},
	}
}

func TestReportContract(t *testing.T) {
	got, err := json.MarshalIndent(contractReport(), "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	got = append(got, '\n')
	path := filepath.Join("testdata", "report-schema1.json")
	if *updateContract {
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run go test -update-contract to create it)", err)
	}
	// Git may check the file out with Windows line endings.
	if string(got) != strings.ReplaceAll(string(want), "\r\n", "\n") {
		t.Fatalf("the report format changed: update Aegis Cloud's schema and run go test -update-contract\n%s", got)
	}
}

func TestScheduleMapping(t *testing.T) {
	for _, tc := range []struct {
		schedule *v1.Schedule
		want     Schedule
	}{
		{nil, Schedule{Kind: "disabled"}},
		{&v1.Schedule{Schedule: &v1.Schedule_Cron{Cron: ""}}, Schedule{Kind: "disabled"}},
		{&v1.Schedule{Schedule: &v1.Schedule_MaxFrequencyHours{MaxFrequencyHours: -1}}, Schedule{Kind: "disabled"}},
		{&v1.Schedule{Schedule: &v1.Schedule_Cron{Cron: "*/15 * * * *"}, Clock: v1.Schedule_CLOCK_DEFAULT}, Schedule{Kind: "cron", Expression: "*/15 * * * *", Clock: "local"}},
		{&v1.Schedule{Schedule: &v1.Schedule_MaxFrequencyDays{MaxFrequencyDays: 1}, Clock: v1.Schedule_CLOCK_UTC}, Schedule{Kind: "days", Every: 1, Clock: "utc"}},
	} {
		if got := scheduleOf(tc.schedule); *got != tc.want {
			t.Errorf("scheduleOf(%v) = %+v, want %+v", tc.schedule, *got, tc.want)
		}
	}
}
