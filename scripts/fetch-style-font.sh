#!/usr/bin/env bash
# Fetch the pinned CJK font the style golden test renders with.
#
# The font is an 8 MB binary kept OUT of git; it's downloaded on demand both
# locally (`make style-font`, run automatically by `make style-test`) and in CI.
# Determinism is what makes the golden comparison meaningful, so the download is
# pinned two ways:
#   - an immutable release tag (not a moving branch), and
#   - a SHA-256 of the exact bytes the committed goldens were generated with.
# If upstream ever serves different bytes, the checksum check fails loudly here
# instead of silently shifting every rendered pixel and turning the style guard
# into noise.
#
# Idempotent: if the font already exists with the right checksum (e.g. a warm CI
# cache or a previous local run), it does nothing.
set -euo pipefail

FONT_TAG="Sans2.004"
FONT_URL="https://github.com/googlefonts/noto-cjk/raw/${FONT_TAG}/Sans/SubsetOTF/SC/NotoSansSC-Regular.otf"
FONT_SHA256="faa6c9df652116dde789d351359f3d7e5d2285a2b2a1f04a2d7244df706d5ea9"

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
DEST="${REPO_ROOT}/internal/video/testdata/fonts/NotoSansSC-Regular.otf"

# sha256 of a file, portable across macOS (shasum) and Linux (sha256sum).
sha256_of() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | awk '{print $1}'
  else
    shasum -a 256 "$1" | awk '{print $1}'
  fi
}

if [ -f "$DEST" ] && [ "$(sha256_of "$DEST")" = "$FONT_SHA256" ]; then
  echo "style font already present and verified: $DEST"
  exit 0
fi

echo "fetching style font ${FONT_TAG} -> $DEST"
mkdir -p "$(dirname "$DEST")"
TMP="$(mktemp)"
trap 'rm -f "$TMP"' EXIT
curl -fsSL --retry 3 --max-time 120 -o "$TMP" "$FONT_URL"

GOT="$(sha256_of "$TMP")"
if [ "$GOT" != "$FONT_SHA256" ]; then
  echo "ERROR: style font checksum mismatch" >&2
  echo "  url:      $FONT_URL" >&2
  echo "  expected: $FONT_SHA256" >&2
  echo "  got:      $GOT" >&2
  echo "Upstream may have changed. If this is an intentional font update, set the" >&2
  echo "new checksum here and regenerate goldens with: make style-golden" >&2
  exit 1
fi

mv "$TMP" "$DEST"
trap - EXIT
echo "style font verified and installed."
