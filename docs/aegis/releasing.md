# Releasing Aegis Agent

There is no CI: a release is built by hand, on Linux, from the commit it is tagged at, so the published files match the published source.

## Requirements

- Go (the version in `go.mod`), Node.js 22 or later with pnpm, git, curl, coreutils (`sha256sum`), bzip2 and GNU tar.
- For the image: Docker with buildx and QEMU (to build `linux/arm64` on another machine), logged in to Docker Hub as `lephianhdev386ht`.

## Steps

1. Add the version to `CHANGELOG.md`, commit, and tag the commit `vX.Y.Z`.
2. From a clean checkout of that tag, build the files and the image:

   ```bash
   scripts/aegis-release.sh X.Y.Z --push-image
   ```

   The script builds the web interface and the `linux/amd64` and `linux/arm64` binaries, downloads restic (the version Aegis Agent requires, `RequiredResticVersion` in `internal/resticinstaller`, checked against restic's own `SHA256SUMS`), writes the two archives, `install.sh`, `uninstall.sh` and `SHA256SUMS` to `dist/aegis/`, and pushes `lephianhdev386ht/aegis-agent:X.Y.Z` and `:latest`.
3. Check that the image has both platforms and runs on each:

   ```bash
   docker buildx imagetools inspect lephianhdev386ht/aegis-agent:X.Y.Z
   docker run --rm --platform linux/arm64 --entrypoint /aegis-agent lephianhdev386ht/aegis-agent:X.Y.Z --version
   ```

4. Push the commit and the tag. Create a draft GitHub release for the tag and upload the two archives, `install.sh`, `uninstall.sh` and `SHA256SUMS` from `dist/aegis/`.
5. Download the assets from the draft, run `sha256sum -c SHA256SUMS` on them, then publish the release as the latest one. Until it is published, `releases/latest/download/install.sh` keeps pointing at the previous release, whose archives are all in place.

Aegis Cloud reads the latest release from the GitHub API and caches it for 30 minutes. From then on, the Add server wizard installs the new version, and servers that report an older one show an Update badge.
