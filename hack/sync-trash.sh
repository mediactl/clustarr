#!/usr/bin/env bash
# Vendors the subset of TRaSH-Guides/Guides this task needs: the Radarr and
# Sonarr custom-format and quality-profile JSON. Sparse, blobless clone at a
# pinned commit -- this is the one network fetch in all of Phase B, and it
# runs here, once, never inside `go test`.
set -euo pipefail

TRASH_COMMIT="${TRASH_COMMIT:?set TRASH_COMMIT to the commit sha to pin}"
DEST="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)/testdata/trash"

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

git clone --filter=blob:none --no-checkout https://github.com/TRaSH-Guides/Guides "$tmp"
git -C "$tmp" sparse-checkout set docs/json/radarr/cf docs/json/radarr/quality-profiles docs/json/sonarr/cf docs/json/sonarr/quality-profiles
git -C "$tmp" checkout "$TRASH_COMMIT"

rm -rf "$DEST/docs"
mkdir -p "$DEST/docs/json"
cp -r "$tmp/docs/json/radarr" "$DEST/docs/json/radarr"
cp -r "$tmp/docs/json/sonarr" "$DEST/docs/json/sonarr"

printf '%s\n%s\n' "$TRASH_COMMIT" "$(date -u +%Y-%m-%dT%H:%M:%SZ)" > "$DEST/COMMIT"

echo "Vendored TRaSH-Guides/Guides @ $TRASH_COMMIT into $DEST"
