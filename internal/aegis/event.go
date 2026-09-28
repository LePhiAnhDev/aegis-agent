package aegis

import (
	"fmt"
	"io"
	"math"
	"regexp"
	"strconv"
	"time"
	"unicode/utf8"

	v1 "github.com/garethgeorge/backrest/gen/go/v1"
)

// SchemaVersion of the report format Aegis Cloud reads (POST /v1/agent/reports).
const SchemaVersion = 1

// Limits Aegis Cloud applies; values are cut to fit instead of being rejected.
const (
	maxEventsPerReport = 200
	maxDetailBytes     = 8 * 1024
	maxMessageBytes    = 2000
	maxFileErrors      = 20
)

// Report is one POST to Aegis Cloud.
type Report struct {
	Schema    int        `json:"schema"`
	SentAt    string     `json:"sentAt"`
	Agent     AgentInfo  `json:"agent"`
	Events    []Event    `json:"events"`
	Inventory *Inventory `json:"inventory,omitempty"`
}

// AgentInfo describes the server; sent in every report.
type AgentInfo struct {
	InstallID     string `json:"installId"`
	InstanceID    string `json:"instanceId"`
	Hostname      string `json:"hostname,omitempty"`
	OS            string `json:"os,omitempty"`
	Arch          string `json:"arch,omitempty"`
	AgentVersion  string `json:"agentVersion,omitempty"`
	ResticVersion string `json:"resticVersion,omitempty"`
	Runtime       string `json:"runtime,omitempty"`
}

// RepoRef names the repository an event happened in, by storage bucket and folder.
type RepoRef struct {
	ID     string `json:"id"`
	GUID   string `json:"guid,omitempty"`
	Bucket string `json:"bucket"`
	Prefix string `json:"prefix"`
}

// EventStats are restic's backup summary.
type EventStats struct {
	BytesProcessed  int64   `json:"bytesProcessed"`
	BytesAdded      int64   `json:"bytesAdded"`
	FilesNew        int64   `json:"filesNew"`
	FilesChanged    int64   `json:"filesChanged"`
	FilesUnmodified int64   `json:"filesUnmodified"`
	FilesProcessed  int64   `json:"filesProcessed"`
	DurationSeconds float64 `json:"durationSeconds"`
}

// FileError is a file a backup could not read: its path and restic's reason.
type FileError struct {
	Item    string `json:"item"`
	Message string `json:"message"`
}

// Event is one operation that started or finished.
type Event struct {
	Key            string      `json:"key"`
	OpID           int64       `json:"opId"`
	FlowID         int64       `json:"flowId,omitempty"`
	Kind           string      `json:"kind"`
	Phase          string      `json:"phase"`
	Status         string      `json:"status,omitempty"`
	PlanID         string      `json:"planId"`
	Repo           RepoRef     `json:"repo"`
	StartedAt      string      `json:"startedAt"`
	FinishedAt     string      `json:"finishedAt,omitempty"`
	SnapshotID     string      `json:"snapshotId,omitempty"`
	Stats          *EventStats `json:"stats,omitempty"`
	BytesReclaimed *int64      `json:"bytesReclaimed,omitempty"`
	Message        string      `json:"message,omitempty"`
	Detail         string      `json:"detail,omitempty"`
	FileErrors     []FileError `json:"fileErrors,omitempty"`
}

const (
	phaseStarted  = "started"
	phaseFinished = "finished"
	// systemPlanID is the plan of maintenance Aegis Agent schedules itself (prune, check).
	systemPlanID = "_system_"
)

// logReader opens an operation's log (logstore.LogStore).
type logReader interface {
	Open(id string) (io.ReadCloser, error)
}

func eventKey(installID string, opID int64, phase string) string {
	return fmt.Sprintf("%s:%d:%s", installID, opID, phase)
}

// operationKind is what Aegis Cloud records of an operation; "" for the rest
// (snapshot indexing, stats, hooks, restores and commands are not reported).
func operationKind(op *v1.Operation) string {
	switch op.Op.(type) {
	case *v1.Operation_OperationBackup:
		if op.GetOperationBackup().GetDryRun() {
			return ""
		}
		return "backup"
	case *v1.Operation_OperationPrune:
		return "prune"
	case *v1.Operation_OperationCheck:
		return "check"
	case *v1.Operation_OperationForget:
		return "forget"
	}
	return ""
}

// resultOf maps a finished operation's status; "" while it has not finished.
func resultOf(status v1.OperationStatus) string {
	switch status {
	case v1.OperationStatus_STATUS_SUCCESS:
		return "success"
	case v1.OperationStatus_STATUS_WARNING:
		return "warning"
	case v1.OperationStatus_STATUS_ERROR,
		v1.OperationStatus_STATUS_SYSTEM_CANCELLED,
		v1.OperationStatus_STATUS_USER_CANCELLED:
		return "failed"
	}
	return ""
}

// phasesOf lists the events an operation in its current state produces.
func phasesOf(op *v1.Operation) []string {
	kind := operationKind(op)
	if kind == "" {
		return nil
	}
	if resultOf(op.Status) != "" {
		return []string{phaseFinished}
	}
	if kind == "backup" && op.Status == v1.OperationStatus_STATUS_INPROGRESS {
		return []string{phaseStarted}
	}
	return nil
}

func isoMs(ms int64) string {
	return time.UnixMilli(ms).UTC().Format(time.RFC3339Nano)
}

// clamp cuts a string to at most limit bytes, on a character boundary.
func clamp(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	cut := limit
	for cut > 0 && !utf8.RuneStart(value[cut]) {
		cut--
	}
	return value[:cut]
}

// tail keeps the last limit bytes, on a character boundary: restic's reason comes last.
func tail(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	start := len(value) - limit
	for start < len(value) && !utf8.RuneStart(value[start]) {
		start++
	}
	return value[start:]
}

// readLog returns an operation's log, or "" when it is gone (logs expire).
func readLog(logs logReader, id string) string {
	if logs == nil || id == "" {
		return ""
	}
	reader, err := logs.Open(id)
	if err != nil {
		return ""
	}
	defer reader.Close()
	data, err := io.ReadAll(io.LimitReader(reader, 4<<20))
	if err != nil {
		return ""
	}
	return string(data)
}

// Backrest writes each restic command line to the operation log
// (`command: "/bin/restic backup -r … /srv"`) and quotes it in failure
// messages (`command "…" failed: …`). Flags can carry secrets, so reports keep
// restic's own output and never the command.
var (
	commandLogLine = regexp.MustCompile(`(?m)^command: "(?:[^"\\]|\\.)*"\r?\n?`)
	quotedCommand  = regexp.MustCompile(`command "(?:[^"\\]|\\.)*" failed`)
)

func withoutCommands(text string) string {
	text = commandLogLine.ReplaceAllString(text, "")
	return quotedCommand.ReplaceAllString(text, "restic failed")
}

// totalPrune reads restic's "total prune:  5 blobs / 2.862 MiB" line.
var totalPrune = regexp.MustCompile(`(?m)^total prune:\s+\d+ blobs / ([\d.]+) (B|KiB|MiB|GiB|TiB)\s*$`)

var byteUnits = map[string]float64{
	"B":   1,
	"KiB": 1 << 10,
	"MiB": 1 << 20,
	"GiB": 1 << 30,
	"TiB": 1 << 40,
}

// parseReclaimed is the space a prune freed, from restic's output; nil when
// the output does not say (never a guess).
func parseReclaimed(output string) *int64 {
	match := totalPrune.FindStringSubmatch(output)
	if match == nil {
		return nil
	}
	value, err := strconv.ParseFloat(match[1], 64)
	if err != nil {
		return nil
	}
	bytes := int64(math.Round(value * byteUnits[match[2]]))
	return &bytes
}

// buildEvent turns an operation into the event of a phase; false when it no
// longer produces one (it changed, or it is not reported at all).
func buildEvent(op *v1.Operation, phase, installID string, repo RepoRef, logs logReader, now time.Time) (Event, bool) {
	kind := operationKind(op)
	if kind == "" {
		return Event{}, false
	}
	result := resultOf(op.Status)
	if phase == phaseFinished && result == "" {
		return Event{}, false
	}
	if phase == phaseStarted && kind != "backup" {
		return Event{}, false
	}

	startedMs := op.UnixTimeStartMs
	if startedMs <= 0 {
		startedMs = now.UnixMilli()
	}
	planID := op.PlanId
	if planID == "" {
		planID = systemPlanID
	}
	event := Event{
		Key:       eventKey(installID, op.Id, phase),
		OpID:      op.Id,
		FlowID:    op.FlowId,
		Kind:      kind,
		Phase:     phase,
		PlanID:    clamp(planID, 100),
		Repo:      repo,
		StartedAt: isoMs(startedMs),
	}
	if phase == phaseStarted {
		return event, true
	}

	endMs := op.UnixTimeEndMs
	if endMs <= 0 {
		// Not recorded: finished no earlier than it started, at the latest now.
		endMs = max(startedMs, now.UnixMilli())
	}
	event.Status = result
	event.FinishedAt = isoMs(endMs)

	var output string
	switch kind {
	case "backup":
		backup := op.GetOperationBackup()
		if summary := backup.GetLastStatus().GetSummary(); summary != nil {
			event.Stats = &EventStats{
				BytesProcessed:  summary.TotalBytesProcessed,
				BytesAdded:      summary.DataAdded,
				FilesNew:        summary.FilesNew,
				FilesChanged:    summary.FilesChanged,
				FilesUnmodified: summary.FilesUnmodified,
				FilesProcessed:  summary.TotalFilesProcessed,
				DurationSeconds: summary.TotalDuration,
			}
			event.SnapshotID = summary.SnapshotId
		}
		for _, fileError := range backup.GetErrors() {
			if len(event.FileErrors) == maxFileErrors {
				break
			}
			event.FileErrors = append(event.FileErrors, FileError{
				Item:    clamp(fileError.Item, 4096),
				Message: clamp(fileError.Message, 1000),
			})
		}
	case "prune":
		output = readLog(logs, op.GetOperationPrune().GetOutputLogref())
		event.BytesReclaimed = parseReclaimed(output)
	case "check":
		output = readLog(logs, op.GetOperationCheck().GetOutputLogref())
	}
	if op.SnapshotId != "" {
		event.SnapshotID = op.SnapshotId
	}
	event.SnapshotID = clamp(event.SnapshotID, 64)

	if result != "success" {
		message := op.DisplayMessage
		if message == "" {
			switch op.Status {
			case v1.OperationStatus_STATUS_USER_CANCELLED:
				message = "Cancelled by the user."
			case v1.OperationStatus_STATUS_SYSTEM_CANCELLED:
				message = "Cancelled by Aegis Agent."
			}
		}
		event.Message = clamp(withoutCommands(message), maxMessageBytes)
		if output == "" {
			output = readLog(logs, op.Logref)
		}
		event.Detail = tail(withoutCommands(output), maxDetailBytes)
	}
	return event, true
}
