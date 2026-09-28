#!/usr/bin/env bash
# Builds the files of an Aegis Agent release from the checked-out commit, on
# Linux: see docs/aegis/releasing.md.
#
#   scripts/aegis-release.sh 1.0.0 [--push-image]
#
# Output in dist/aegis/:
#   aegis-agent_Linux_x86_64.tar.gz, aegis-agent_Linux_arm64.tar.gz
#       aegis-agent, restic, install.sh, uninstall.sh, LICENSE,
#       LICENSE.restic, NOTICE, README.md
#   install.sh, uninstall.sh, SHA256SUMS      (release assets)
#   docker/                                   (context for Dockerfile.alpine)
# With --push-image, also builds lephianhdev386ht/aegis-agent:<version> and
# :latest for linux/amd64 and linux/arm64 and pushes them (docker buildx).
set -euo pipefail

VERSION=${1:-}
PUSH_IMAGE=false
[ "${2:-}" = --push-image ] && PUSH_IMAGE=true
if ! [[ $VERSION =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
  echo "usage: scripts/aegis-release.sh X.Y.Z [--push-image]" >&2
  exit 1
fi

IMAGE=lephianhdev386ht/aegis-agent
ARCHES="amd64 arm64"
PACKAGE_FILES="aegis-agent restic install.sh uninstall.sh LICENSE LICENSE.restic NOTICE README.md"

cd "$(dirname "$0")/.."
OUT=$PWD/dist/aegis
# The version Aegis Agent requires of the restic next to it.
RESTIC_VERSION=$(sed -n 's/^[[:space:]]*RequiredResticVersion = "\(.*\)"$/\1/p' internal/resticinstaller/resticinstaller.go)
[ -n "$RESTIC_VERSION" ] || { echo "RequiredResticVersion not found" >&2; exit 1; }
for tool in git go pnpm curl sha256sum bunzip2 tar; do
  command -v "$tool" >/dev/null || { echo "missing tool: $tool" >&2; exit 1; }
done
if [ -n "$(git status --porcelain)" ] && [ "${ALLOW_DIRTY:-}" != 1 ]; then
  echo "the working tree has changes; a release is built from a committed tree" >&2
  exit 1
fi

COMMIT=$(git rev-parse HEAD)
# Archives carry the commit time, so the same commit gives the same bytes.
export SOURCE_DATE_EPOCH=${SOURCE_DATE_EPOCH:-$(git log -1 --format=%ct)}
export GZIP=-n
rm -rf "$OUT"
mkdir -p "$OUT"
WORK=$(mktemp -d)
trap 'rm -rf "$WORK"' EXIT

echo "==> Web interface"
# The interface shows this version in its header; without it, it presents
# itself as a development build.
export BACKREST_BUILD_VERSION=$VERSION
pnpm --dir webui install --frozen-lockfile
pnpm --dir webui run build

echo "==> Binaries ($VERSION, $COMMIT)"
for arch in $ARCHES; do
  mkdir -p "$WORK/$arch"
  CGO_ENABLED=0 GOOS=linux GOARCH=$arch go build -trimpath \
    -ldflags "-s -w -X main.version=$VERSION -X main.commit=$COMMIT" \
    -o "$WORK/$arch/aegis-agent" ./cmd/backrest
  CGO_ENABLED=0 GOOS=linux GOARCH=$arch go build -trimpath -ldflags "-s -w" \
    -o "$WORK/$arch/docker-entrypoint" ./cmd/docker-entrypoint
done

echo "==> restic $RESTIC_VERSION"
restic_url=https://github.com/restic/restic/releases/download/v$RESTIC_VERSION
curl -fsSL -o "$WORK/restic-SHA256SUMS" "$restic_url/SHA256SUMS"
for arch in $ARCHES; do
  file=restic_${RESTIC_VERSION}_linux_$arch.bz2
  curl -fsSL -o "$WORK/$file" "$restic_url/$file"
  (cd "$WORK" && grep "  $file\$" restic-SHA256SUMS | sha256sum -c --quiet -)
  bunzip2 -c "$WORK/$file" >"$WORK/$arch/restic"
  chmod 0755 "$WORK/$arch/restic"
done

echo "==> Archives"
for arch in $ARCHES; do
  case $arch in
    amd64) name=aegis-agent_Linux_x86_64 ;;
    arm64) name=aegis-agent_Linux_arm64 ;;
  esac
  package=$WORK/package-$arch
  mkdir -p "$package"
  install -m 0755 "$WORK/$arch/aegis-agent" "$WORK/$arch/restic" install.sh uninstall.sh "$package/"
  install -m 0644 LICENSE NOTICE README.md build/licenses/LICENSE.restic "$package/"
  # shellcheck disable=SC2086 # one word per file
  tar --owner=0 --group=0 --numeric-owner --mtime="@$SOURCE_DATE_EPOCH" \
    -C "$package" -czf "$OUT/$name.tar.gz" $PACKAGE_FILES
done
install -m 0755 install.sh uninstall.sh "$OUT/"
(cd "$OUT" && sha256sum aegis-agent_Linux_*.tar.gz install.sh uninstall.sh >SHA256SUMS)

echo "==> Docker context"
for arch in $ARCHES; do
  mkdir -p "$OUT/docker/linux/$arch"
  install -m 0755 "$WORK/$arch/aegis-agent" "$WORK/$arch/docker-entrypoint" "$WORK/$arch/restic" \
    "$OUT/docker/linux/$arch/"
done
mkdir -p "$OUT/docker/licenses"
install -m 0644 LICENSE NOTICE build/licenses/LICENSE.restic "$OUT/docker/licenses/"
install -m 0644 Dockerfile.alpine "$OUT/docker/Dockerfile"

if [ "$PUSH_IMAGE" = true ]; then
  echo "==> Image $IMAGE:$VERSION"
  docker buildx build --platform linux/amd64,linux/arm64 --pull --provenance=false \
    --build-arg VERSION="$VERSION" -t "$IMAGE:$VERSION" -t "$IMAGE:latest" --push "$OUT/docker"
fi

echo "==> Done"
cat "$OUT/SHA256SUMS"
