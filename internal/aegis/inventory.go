package aegis

import (
	"errors"

	v1 "github.com/garethgeorge/backrest/gen/go/v1"
	"github.com/garethgeorge/backrest/internal/oplog"
)

// Inventory mirrors the server's repositories on Aegis Cloud storages and
// the plans that write to them.
type Inventory struct {
	Repos []InventoryRepo `json:"repos"`
	Plans []InventoryPlan `json:"plans"`
}

// InventoryRepo is a repository with restic's latest statistics of it.
type InventoryRepo struct {
	RepoRef
	Stats *RepoStats `json:"stats,omitempty"`
}

// RepoStats are restic's repository statistics (Aegis Agent collects them after pruning).
type RepoStats struct {
	TotalSize     int64  `json:"totalSize"`
	SnapshotCount int64  `json:"snapshotCount"`
	At            string `json:"at,omitempty"`
}

// InventoryPlan is a backup plan as configured in Aegis Agent.
type InventoryPlan struct {
	ID                string    `json:"id"`
	RepoID            string    `json:"repoId"`
	Paths             []string  `json:"paths"`
	Schedule          *Schedule `json:"schedule"`
	SnapshotCount     *int64    `json:"snapshotCount,omitempty"`
	LastSnapshotBytes *int64    `json:"lastSnapshotBytes,omitempty"`
}

// Schedule is a plan's schedule: cron, every N hours or days, or disabled.
type Schedule struct {
	Kind       string `json:"kind"`
	Expression string `json:"expression,omitempty"`
	Every      int32  `json:"every,omitempty"`
	Clock      string `json:"clock,omitempty"`
}

type opQuerier interface {
	Query(q oplog.Query, f func(*v1.Operation) error) error
}

// aegisRepos are the configured repositories on Aegis Cloud storages, by id.
func aegisRepos(cfg *v1.Config) map[string]RepoRef {
	repos := make(map[string]RepoRef)
	for _, repo := range cfg.GetRepos() {
		r2, ok := ParseR2RepoURI(repo.GetUri())
		if !ok {
			continue
		}
		repos[repo.GetId()] = RepoRef{
			ID:     clamp(repo.GetId(), 100),
			GUID:   clamp(repo.GetGuid(), 128),
			Bucket: r2.Bucket,
			Prefix: clamp(r2.Prefix, 512),
		}
	}
	return repos
}

func clockOf(clock v1.Schedule_Clock) string {
	switch clock {
	case v1.Schedule_CLOCK_UTC:
		return "utc"
	case v1.Schedule_CLOCK_LAST_RUN_TIME:
		return "last-run"
	default:
		return "local"
	}
}

// scheduleOf maps Aegis Agent's schedule; anything that never runs is "disabled".
func scheduleOf(schedule *v1.Schedule) *Schedule {
	clock := clockOf(schedule.GetClock())
	switch value := schedule.GetSchedule().(type) {
	case *v1.Schedule_Cron:
		if value.Cron != "" {
			return &Schedule{Kind: "cron", Expression: clamp(value.Cron, 200), Clock: clock}
		}
	case *v1.Schedule_MaxFrequencyHours:
		if value.MaxFrequencyHours > 0 {
			return &Schedule{Kind: "hours", Every: value.MaxFrequencyHours, Clock: clock}
		}
	case *v1.Schedule_MaxFrequencyDays:
		if value.MaxFrequencyDays > 0 {
			return &Schedule{Kind: "days", Every: value.MaxFrequencyDays, Clock: clock}
		}
	}
	return &Schedule{Kind: "disabled"}
}

var errStop = oplog.ErrStopIteration

// latestStats is the newest successful statistics run of a repository.
func latestStats(ops opQuerier, repoGUID string) (*RepoStats, error) {
	if repoGUID == "" {
		return nil, nil
	}
	var stats *RepoStats
	err := ops.Query(oplog.Query{}.SetRepoGUID(repoGUID).SetReversed(true), func(op *v1.Operation) error {
		result := op.GetOperationStats().GetStats()
		if result == nil || op.Status != v1.OperationStatus_STATUS_SUCCESS {
			return nil
		}
		stats = &RepoStats{
			TotalSize:     result.TotalSize,
			SnapshotCount: result.SnapshotCount,
			At:            isoMs(op.UnixTimeEndMs),
		}
		return errStop
	})
	if err != nil && !errors.Is(err, errStop) {
		return nil, err
	}
	return stats, nil
}

// planSnapshots counts a plan's snapshots that are not forgotten, and the
// size of its newest successful backup.
func planSnapshots(ops opQuerier, planID, repoGUID string) (count int64, lastBytes *int64, err error) {
	query := oplog.Query{}.SetPlanID(planID).SetReversed(true)
	if repoGUID != "" {
		query = query.SetRepoGUID(repoGUID)
	}
	err = ops.Query(query, func(op *v1.Operation) error {
		if indexed := op.GetOperationIndexSnapshot(); indexed != nil && !indexed.Forgot {
			count++
		}
		if lastBytes == nil && operationKind(op) == "backup" && resultOf(op.Status) != "failed" && resultOf(op.Status) != "" {
			if summary := op.GetOperationBackup().GetLastStatus().GetSummary(); summary != nil {
				bytes := summary.TotalBytesProcessed
				lastBytes = &bytes
			}
		}
		return nil
	})
	return count, lastBytes, err
}

// buildInventory lists the Aegis repositories and the plans that use them.
// Nothing else of the configuration is included.
func buildInventory(cfg *v1.Config, ops opQuerier) (*Inventory, error) {
	repos := aegisRepos(cfg)
	inventory := &Inventory{Repos: []InventoryRepo{}, Plans: []InventoryPlan{}}
	for _, repo := range cfg.GetRepos() {
		ref, ok := repos[repo.GetId()]
		if !ok {
			continue
		}
		stats, err := latestStats(ops, repo.GetGuid())
		if err != nil {
			return nil, err
		}
		inventory.Repos = append(inventory.Repos, InventoryRepo{RepoRef: ref, Stats: stats})
	}

	for _, plan := range cfg.GetPlans() {
		repo, ok := repos[plan.GetRepo()]
		if !ok {
			continue
		}
		count, lastBytes, err := planSnapshots(ops, plan.GetId(), repo.GUID)
		if err != nil {
			return nil, err
		}
		paths := make([]string, 0, len(plan.GetPaths()))
		for _, path := range plan.GetPaths() {
			if len(paths) == 200 {
				break
			}
			paths = append(paths, clamp(path, 4096))
		}
		inventory.Plans = append(inventory.Plans, InventoryPlan{
			ID:                clamp(plan.GetId(), 100),
			RepoID:            repo.ID,
			Paths:             paths,
			Schedule:          scheduleOf(plan.GetSchedule()),
			SnapshotCount:     &count,
			LastSnapshotBytes: lastBytes,
		})
		if len(inventory.Plans) == 200 {
			break
		}
	}
	return inventory, nil
}
