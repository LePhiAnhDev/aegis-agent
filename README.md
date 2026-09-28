<p align="center">
  <img src="./webui/assets/logo.png" width="120" alt="Aegis Agent" />
</p>

<h1 align="center">Aegis Agent</h1>

Aegis Agent runs on your server and backs it up with [restic](https://restic.net) to the storages that [Aegis Cloud](https://aegis.ailabx.vn) issues on Cloudflare R2. You create repositories, backup plans, schedules and restores in its web interface, on the server itself. Aegis Agent reports every operation to Aegis Cloud, which shows the history, how well each server is protected, and alerts you on Telegram.

Aegis Agent is based on [Backrest](https://github.com/garethgeorge/backrest) and is distributed under the GPL-3.0 license. See [NOTICE](./NOTICE).

## How it works

- **Your password, your keys.** Backups are encrypted on your server with a repository password you choose in Aegis Agent. It never leaves the server, and Aegis Cloud never asks for it.
- **One-way reporting.** Aegis Agent sends reports to Aegis Cloud. Aegis Cloud never starts, stops or changes anything on the server.
- **Everything happens on the server.** Plans, schedules, retention, checks and restores are configured and run in Aegis Agent.

## Install

Add the server in Aegis Cloud (**Servers → Add server**). The install step gives you a `docker-compose.yml`, or a Linux install command, with the server's keys already filled in. Releases and their checksums are published on the [releases page](https://github.com/LePhiAnhDev/aegis-agent/releases). Aegis Agent runs on Linux, on x86_64 and arm64.

### Linux (systemd)

The command from Aegis Cloud, run as root, writes the server's settings to `/etc/aegis-agent/agent.env` (readable by root only) and runs [`install.sh`](./install.sh), which:

- downloads the release archive for the server's CPU and checks it against the release's `SHA256SUMS` before installing anything;
- puts Aegis Agent and the restic it runs in `/opt/aegis-agent` (`aegis-agent` is linked into `/usr/local/bin`);
- keeps the configuration in `/etc/aegis-agent`, the operation history in `/var/lib/aegis-agent` and restic's cache in `/var/cache/aegis-agent`;
- starts the root systemd service `aegis-agent` (`systemctl status aegis-agent`, `journalctl -u aegis-agent`).

To upgrade, run the same command with the new `--version`: the settings and history stay. `sh /opt/aegis-agent/uninstall.sh` removes Aegis Agent; add `--purge` to delete `/etc/aegis-agent` and `/var/lib/aegis-agent` too.

Without internet access from the server, copy `aegis-agent_Linux_<x86_64|arm64>.tar.gz` and `SHA256SUMS` from the release, check them with `sha256sum -c --ignore-missing SHA256SUMS`, extract the archive, and run `sh install.sh` as root in that folder.

### Docker

The `docker-compose.yml` from Aegis Cloud runs the image `lephianhdev386ht/aegis-agent` (linux/amd64 and linux/arm64) with the settings in its environment. It mounts the host folders to back up read-only under `/userdata`, keeps `/data`, `/config` and `/cache` in `./aegis-agent/`, gives restores a writable `./aegis-agent/restores`, and publishes the interface on the host's `127.0.0.1:9898`. The image holds the license texts in `/licenses`.

## Open the web interface

The interface listens on `127.0.0.1:9898` of the server only. From your computer, open an SSH tunnel and browse to <http://localhost:9898>:

```bash
ssh -L 9898:localhost:9898 you@your-server
```

On first start, set the instance ID and create the login for the interface.

## Restore

Restores run in Aegis Agent: open the repository, choose a snapshot, browse to a file or folder and choose **Restore**. On Linux the suggested target is next to the original. The Docker image backs up folders mounted read-only under `/userdata`, so it suggests a folder under `/restores`, which is `./aegis-agent/restores` on the server. With an empty target, restic restores into the downloads folder of the interface's user (`$HOME/Downloads`), and the finished restore offers its files for download in the operation's details. A restore never writes into a folder that already exists.

## Configuration

The install files from Aegis Cloud set the first four rows; the Linux service and the image choose the paths and the listen address.

| Variable | Purpose |
| --- | --- |
| `AEGIS_REPORT_URL`, `AEGIS_REPORT_TOKEN` | Where Aegis Agent reports and the token that signs its reports; without them it runs as a plain backup tool |
| `AEGIS_INSTANCE_ID` | Instance ID suggested on first start |
| `AEGIS_REPOSITORIES` | The Aegis Cloud storages this server may write to, offered when adding a repository |
| `AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY`, `AWS_DEFAULT_REGION` | Storage access key issued by Aegis Cloud (restic reads them for R2 repositories) |
| `AEGIS_HEARTBEAT_INTERVAL` | How often Aegis Agent reports when idle (default `5m`, at least `1m`) |
| `BACKREST_PORT` | Address the web interface listens on (Linux service: `127.0.0.1:9898`; in the container `0.0.0.0:9898`, published on the host's `127.0.0.1:9898`) |
| `BACKREST_CONFIG` | Path of the configuration file |
| `BACKREST_DATA` | Directory for the operation history and logs |
| `BACKREST_RESTIC_COMMAND` | Path of the restic binary to use |

## Command hooks

Monitoring and alerts come from Aegis Cloud. Command hooks run your own scripts around a backup, for example a database dump: see [docs/aegis/hooks.md](./docs/aegis/hooks.md).

## Build from source

Requirements: Go 1.26 or later, Node.js 22 or later, pnpm.

```bash
cd webui && pnpm install && pnpm run build && cd ..
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o aegis-agent ./cmd/backrest
```

Release archives and images are built with `scripts/aegis-release.sh`: see [docs/aegis/releasing.md](./docs/aegis/releasing.md).

## License

Aegis Agent is free software under the GNU General Public License v3.0 ([LICENSE](./LICENSE)). It is a modified version of Backrest by Gareth George and the Backrest contributors; the changes are listed in [NOTICE](./NOTICE).
