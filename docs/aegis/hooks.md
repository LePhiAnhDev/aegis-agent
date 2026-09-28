# Command hooks in Aegis Agent

Aegis Agent reports every backup, prune, check and forget to Aegis Cloud on its own. You do not need a hook for monitoring or alerts: Aegis Cloud keeps the history and sends alerts to Telegram.

Command hooks are for your own automation around a backup, for example dumping a database before the snapshot starts, or pausing a service while it runs. A hook can be attached to a plan or to a repository.

This page is adapted from the Backrest documentation (GPL-3.0), which Aegis Agent is based on.

## Events

A hook runs when one of the events you select happens.

| Event | When it runs |
| --- | --- |
| `CONDITION_SNAPSHOT_START` | Before a backup starts. The backup waits for the command, and the error policy decides what happens if it fails. |
| `CONDITION_SNAPSHOT_END` | After a backup finishes, whatever the result. |
| `CONDITION_SNAPSHOT_SUCCESS` | After a backup succeeds. |
| `CONDITION_SNAPSHOT_WARNING` | After a backup finishes with warnings (some files could not be read). |
| `CONDITION_SNAPSHOT_ERROR` | After a backup fails. |
| `CONDITION_SNAPSHOT_SKIPPED` | When a backup is skipped because nothing changed. |
| `CONDITION_PRUNE_START`, `CONDITION_PRUNE_SUCCESS`, `CONDITION_PRUNE_ERROR` | Around a prune. |
| `CONDITION_CHECK_START`, `CONDITION_CHECK_SUCCESS`, `CONDITION_CHECK_ERROR` | Around a repository check. |
| `CONDITION_FORGET_START`, `CONDITION_FORGET_SUCCESS`, `CONDITION_FORGET_ERROR` | Around a forget (retention policy). |
| `CONDITION_ANY_ERROR` | When any operation fails. |

## When a hook fails

| Policy | Effect |
| --- | --- |
| `ON_ERROR_IGNORE` | The operation continues. |
| `ON_ERROR_CANCEL` | The operation is cancelled and later hooks are skipped. |
| `ON_ERROR_FATAL` | The operation fails, and error hooks run. |
| `ON_ERROR_RETRY_1MINUTE`, `ON_ERROR_RETRY_10MINUTES`, `ON_ERROR_RETRY_EXPONENTIAL_BACKOFF` | The operation is retried later. |

Use `ON_ERROR_FATAL` on a `CONDITION_SNAPSHOT_START` hook when the backup is useless without it, for example a database dump. The failed backup is then reported to Aegis Cloud like any other failure.

## Where commands run

- **Docker:** inside the Aegis Agent container. The image includes `sh`, `bash`, `curl`, `ssh` and `docker` (the Docker CLI needs `/var/run/docker.sock` mounted to reach other containers).
- **Linux (systemd):** on the server, as root.

A command starting with `#!` on its first line runs with that interpreter, for example `#!/bin/bash`.

## Template variables

Commands are Go templates rendered before they run.

| Variable | Description | Example |
| --- | --- | --- |
| `Event` | The event that triggered the hook | `{{ .EventName .Event }}` |
| `Task` | Name of the task | `{{ .Task }}` |
| `Repo` | The repository | `{{ .Repo.Id }}` |
| `Plan` | The plan | `{{ .Plan.Id }}` |
| `SnapshotId` | Snapshot created by the backup | `{{ .SnapshotId }}` |
| `SnapshotStats` | Backup statistics | `{{ .SnapshotStats.DataAdded }}` |
| `CurTime` | Current time | `{{ .FormatTime .CurTime }}` |
| `Error` | Error message, when there is one | `{{ .Error }}` |

Helper functions: `.Summary` (a readable summary of the event), `.FormatTime`, `.FormatSizeBytes`, `.ShellEscape` (quote a value for the shell) and `.JsonMarshal`.

## Example: dump PostgreSQL before each backup

Attach this hook to the plan, on `CONDITION_SNAPSHOT_START`, with `ON_ERROR_FATAL`, and include `/var/backups/postgresql` in the plan's paths:

```bash
#!/bin/bash
set -euo pipefail
mkdir -p /var/backups/postgresql
pg_dump --format=custom --file=/var/backups/postgresql/app.dump app
```

More examples: [command hook cookbook](../src/cookbooks/command-hook-examples.md).
