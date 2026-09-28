#!/bin/sh
# Aegis Agent installer for Linux servers with systemd (x86_64, arm64).
#
# From Aegis Cloud (Servers > Add server > Linux), as root:
#   curl -fsSL https://github.com/LePhiAnhDev/aegis-agent/releases/latest/download/install.sh | sh -s -- --version 1.0.0
# From an extracted release archive, as root:
#   sh install.sh
# To remove it (settings and history stay unless --purge is added):
#   sh /opt/aegis-agent/uninstall.sh
#
# Aegis Agent runs as a root systemd service. Its settings, including the
# storage key and report token Aegis Cloud issued, are read from
# /etc/aegis-agent/agent.env, which this script creates only when it is missing
# and never overwrites. Running it again upgrades Aegis Agent in place.
set -eu

REPO_URL=https://github.com/LePhiAnhDev/aegis-agent
INSTALL_DIR=/opt/aegis-agent
BIN_LINK=/usr/local/bin/aegis-agent
CONFIG_DIR=/etc/aegis-agent
ENV_FILE=/etc/aegis-agent/agent.env
DATA_DIR=/var/lib/aegis-agent
CACHE_DIR=/var/cache/aegis-agent
SERVICE=aegis-agent
UNIT_FILE=/etc/systemd/system/aegis-agent.service
PACKAGE_FILES="aegis-agent restic install.sh uninstall.sh LICENSE LICENSE.restic NOTICE README.md"

say() {
  echo "==> $*"
}

fail() {
  echo "aegis-agent install: $*" >&2
  exit 1
}

usage() {
  cat <<'EOF'
Usage: install.sh [--version X.Y.Z]
       install.sh --uninstall [--purge]

  --version X.Y.Z  Download and install this release (default: the files next
                   to this script, or else the latest release).
  --uninstall      Stop and remove Aegis Agent. Its settings stay in
                   /etc/aegis-agent and its history in /var/lib/aegis-agent.
  --purge          With --uninstall, delete those two directories as well.
EOF
}

check_system() {
  [ "$(id -u)" = 0 ] || fail "run this as root, for example with sudo"
  [ "$(uname -s)" = Linux ] || fail "this installer is for Linux; elsewhere, run the Docker image"
  [ -d /run/systemd/system ] || fail "systemd is not running on this system; run the Docker image instead"
  case $(uname -m) in
    x86_64 | amd64) ARCH=x86_64 ;;
    aarch64 | arm64) ARCH=arm64 ;;
    *) fail "unsupported CPU $(uname -m); Aegis Agent is built for x86_64 and arm64" ;;
  esac
}

download() {
  if command -v curl >/dev/null 2>&1; then
    curl -fsSL --retry 3 -o "$2" "$1"
  elif command -v wget >/dev/null 2>&1; then
    wget -q -O "$2" "$1"
  else
    fail "install curl or wget, then run this again"
  fi
}

# Downloads the release archive for this CPU and checks it against the
# release's SHA256SUMS before anything is installed.
fetch_release() {
  if [ -n "$VERSION" ]; then
    base="$REPO_URL/releases/download/v$VERSION"
  else
    base="$REPO_URL/releases/latest/download"
  fi
  archive="aegis-agent_Linux_$ARCH.tar.gz"
  command -v sha256sum >/dev/null 2>&1 || fail "sha256sum is missing (it comes with coreutils)"

  say "Downloading Aegis Agent ${VERSION:-(latest release)} for $ARCH"
  download "$base/$archive" "$WORK/$archive" || fail "could not download $base/$archive"
  download "$base/SHA256SUMS" "$WORK/SHA256SUMS" || fail "could not download $base/SHA256SUMS"

  expected=$(awk -v name="$archive" '$2 == name || $2 == "*" name { print $1 }' "$WORK/SHA256SUMS")
  [ -n "$expected" ] || fail "SHA256SUMS has no entry for $archive"
  actual=$(sha256sum "$WORK/$archive" | cut -d ' ' -f 1)
  [ "$expected" = "$actual" ] || fail "checksum mismatch for $archive (expected $expected, got $actual); nothing was installed"
  say "Checksum verified"

  mkdir "$WORK/package"
  tar -xzf "$WORK/$archive" -C "$WORK/package"
  SOURCE_DIR="$WORK/package"
}

install_files() {
  [ -f "$SOURCE_DIR/aegis-agent" ] && [ -f "$SOURCE_DIR/restic" ] ||
    fail "$SOURCE_DIR does not hold an Aegis Agent release (aegis-agent and restic)"

  install -d -m 0755 "$INSTALL_DIR"
  for file in $PACKAGE_FILES; do
    [ -f "$SOURCE_DIR/$file" ] || continue
    case $file in
      aegis-agent | restic | *.sh) mode=0755 ;;
      *) mode=0644 ;;
    esac
    # Copy, then rename over the old file: a running binary is replaced
    # without "text file busy".
    install -m "$mode" "$SOURCE_DIR/$file" "$INSTALL_DIR/.$file.new"
    mv -f "$INSTALL_DIR/.$file.new" "$INSTALL_DIR/$file"
  done
  ln -sf "$INSTALL_DIR/aegis-agent" "$BIN_LINK"

  install -d -m 0700 "$CONFIG_DIR" "$DATA_DIR" "$CACHE_DIR"
  if [ -f "$ENV_FILE" ]; then
    chmod 0600 "$ENV_FILE"
  else
    install -m 0600 /dev/null "$ENV_FILE"
    MISSING_SETTINGS=true
  fi
}

# Paths and the listen address are set here; agent.env holds everything that
# came from Aegis Cloud and may override these (it is read after them).
write_unit() {
  cat >"$UNIT_FILE" <<EOF
[Unit]
Description=Aegis Agent (restic backups reported to Aegis Cloud)
Documentation=$REPO_URL
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=root
Environment=BACKREST_PORT=127.0.0.1:9898
Environment=BACKREST_CONFIG=$CONFIG_DIR/config.json
Environment=BACKREST_DATA=$DATA_DIR
Environment=XDG_CACHE_HOME=$CACHE_DIR
EnvironmentFile=$ENV_FILE
ExecStart=$INSTALL_DIR/aegis-agent
Restart=on-failure
RestartSec=10s

[Install]
WantedBy=multi-user.target
EOF
  chmod 0644 "$UNIT_FILE"
}

start_service() {
  systemctl daemon-reload
  systemctl enable "$SERVICE" >/dev/null 2>&1
  systemctl restart "$SERVICE"
  attempt=0
  while ! systemctl is-active --quiet "$SERVICE"; do
    attempt=$((attempt + 1))
    [ "$attempt" -lt 10 ] || fail "Aegis Agent did not start; see: journalctl -u $SERVICE"
    sleep 1
  done
}

uninstall() {
  if [ -f "$UNIT_FILE" ]; then
    systemctl disable --now "$SERVICE" >/dev/null 2>&1 || true
    rm -f "$UNIT_FILE"
    systemctl daemon-reload
  fi
  if [ -L "$BIN_LINK" ]; then
    rm -f "$BIN_LINK"
  fi
  rm -rf "$INSTALL_DIR" "$CACHE_DIR"
  if [ "$PURGE" = true ]; then
    rm -rf "$CONFIG_DIR" "$DATA_DIR"
    say "Aegis Agent was removed, with its settings and history."
  else
    say "Aegis Agent was removed. Its settings stay in $CONFIG_DIR and its history in $DATA_DIR;"
    say "run this again with --uninstall --purge to delete them."
  fi
}

main() {
  VERSION=""
  UNINSTALL=false
  PURGE=false
  MISSING_SETTINGS=false
  while [ $# -gt 0 ]; do
    case $1 in
      --version)
        [ $# -ge 2 ] || fail "--version needs a release number, for example --version 1.0.0"
        VERSION=$2
        shift 2
        ;;
      --version=*)
        VERSION=${1#--version=}
        shift
        ;;
      --uninstall)
        UNINSTALL=true
        shift
        ;;
      --purge)
        PURGE=true
        shift
        ;;
      -h | --help)
        usage
        exit 0
        ;;
      *)
        usage >&2
        fail "unknown option: $1"
        ;;
    esac
  done
  VERSION=${VERSION#v}
  case $VERSION in
    *[!0-9.]* | .* | *. | *..*) fail "--version takes a release number such as 1.0.0" ;;
  esac
  [ "$PURGE" = false ] || [ "$UNINSTALL" = true ] || fail "--purge only goes with --uninstall"

  check_system
  if [ "$UNINSTALL" = true ]; then
    uninstall
    exit 0
  fi

  WORK=$(mktemp -d)
  trap 'rm -rf "$WORK"' EXIT

  # Run from an extracted release archive: install the files next to this
  # script, unless a version was asked for or this is the installed copy.
  SOURCE_DIR=""
  case $0 in
    *install.sh) script_dir=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd) ;;
    *) script_dir="" ;;
  esac
  if [ -z "$VERSION" ] && [ -n "$script_dir" ] && [ "$script_dir" != "$INSTALL_DIR" ] &&
    [ -f "$script_dir/aegis-agent" ] && [ -f "$script_dir/restic" ]; then
    SOURCE_DIR=$script_dir
    say "Installing Aegis Agent from $SOURCE_DIR"
  else
    fetch_release
  fi

  upgrade=false
  if [ -f "$UNIT_FILE" ]; then
    upgrade=true
  fi
  install_files
  write_unit
  start_service

  installed=$("$INSTALL_DIR/aegis-agent" --version 2>/dev/null || echo "Aegis Agent")
  if [ "$upgrade" = true ]; then
    say "Upgraded and restarted: $installed"
  else
    say "Installed and started: $installed"
  fi
  # agent.env may move the interface (BACKREST_PORT); show where it listens.
  listen=$(sed -n 's/^BACKREST_PORT=//p' "$ENV_FILE" | tail -n 1 | tr -d "\"'")
  listen=${listen:-127.0.0.1:9898}
  port=${listen##*:}
  echo "    Service:   systemctl status $SERVICE   (logs: journalctl -u $SERVICE)"
  echo "    Interface: http://$listen on this server; from your computer:"
  echo "               ssh -L $port:localhost:$port <user>@<this server>, then open http://localhost:$port"
  echo "    Settings:  $ENV_FILE   History: $DATA_DIR"
  if [ "$MISSING_SETTINGS" = true ]; then
    echo ""
    echo "    $ENV_FILE was empty, so Aegis Agent does not report to Aegis Cloud yet."
    echo "    Add the server in Aegis Cloud, put its settings in that file, then run:"
    echo "    systemctl restart $SERVICE"
  fi
}

# Everything above only defines functions: a download cut short runs nothing.
main "$@"
