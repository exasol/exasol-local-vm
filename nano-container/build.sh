#!/usr/bin/env bash
# Build the Exasol Nano DB container.
#
# Usage: ./build.sh <aarch64|x86_64>
#
# The script downloads the .run for the requested architecture from the URL
# pinned in nano-version.env, verifies the SHA256, builds the container with
# podman, and saves a zstd-compressed tarball under
# <repo>/output/nano-container/.
#
# SHA bootstrap: if the SHA for the requested arch is empty in
# nano-version.env, the script captures the downloaded file's SHA and writes
# it back into nano-version.env. On every subsequent build the SHA is
# verified and any mismatch fails the build loudly.

set -euo pipefail

ARCH="${1:?usage: $0 <aarch64|x86_64>}"
DIR="$(cd "$(dirname "$0")" && pwd)"
ROOT="$(cd "$DIR/.." && pwd)"
ENV_FILE="$DIR/nano-version.env"

if [ ! -f "$ENV_FILE" ]; then
  echo "ERROR: $ENV_FILE not found" >&2
  exit 1
fi

# shellcheck source=./nano-version.env
source "$ENV_FILE"

case "$ARCH" in
  aarch64)
    URL="${NANO_AARCH64_URL:-}"
    SHA_VAR="NANO_AARCH64_SHA"
    PLATFORM="linux/arm64"
    ;;
  x86_64)
    URL="${NANO_X86_64_URL:-}"
    SHA_VAR="NANO_X86_64_SHA"
    PLATFORM="linux/amd64"
    ;;
  *)
    echo "ERROR: unsupported arch '$ARCH' (use aarch64 or x86_64)" >&2
    exit 1
    ;;
esac

if [ -z "$URL" ]; then
  echo "ERROR: URL for $ARCH is empty in $ENV_FILE" >&2
  exit 1
fi
if [ -z "${NANO_RELEASE_TAG:-}" ]; then
  echo "ERROR: NANO_RELEASE_TAG is empty in $ENV_FILE" >&2
  exit 1
fi

EXPECTED="${!SHA_VAR}"
RUN_FILE="$DIR/nano.run"

# Cross-platform in-place sed (BSD/macOS vs GNU).
sed_inplace() {
  if sed --version >/dev/null 2>&1; then
    sed -i "$@"
  else
    sed -i '' "$@"
  fi
}

update_env_sha() {
  local var="$1" sha="$2"
  sed_inplace "s|^${var}=.*|${var}=${sha}|" "$ENV_FILE"
}

sha_of() {
  sha256sum "$1" | cut -d' ' -f1
}

# Fast path: pinned SHA + matching cached file.
if [ -n "$EXPECTED" ] && [ -f "$RUN_FILE" ] && [ "$(sha_of "$RUN_FILE")" = "$EXPECTED" ]; then
  echo "==> Using cached nano.run ($ARCH)"
else
  echo "==> Downloading $URL"
  # The exasol-nano repo is private, so an unauthenticated curl gets 404.
  # Use `gh release download` which authenticates via the gh token.
  # URL format: https://github.com/{OWNER}/{REPO}/releases/download/{TAG}/{ASSET}
  REPO_PATH="${URL#https://github.com/}"
  OWNER_REPO="${REPO_PATH%%/releases/*}"
  ASSET_NAME="${URL##*/}"
  rm -f "$RUN_FILE.tmp"
  gh release download "$NANO_RELEASE_TAG" \
    --repo "$OWNER_REPO" \
    --pattern "$ASSET_NAME" \
    --output "$RUN_FILE.tmp"
  ACTUAL="$(sha_of "$RUN_FILE.tmp")"

  if [ -z "$EXPECTED" ]; then
    echo "==> Capturing SHA for $ARCH (was empty): $ACTUAL"
    update_env_sha "$SHA_VAR" "$ACTUAL"
  elif [ "$ACTUAL" != "$EXPECTED" ]; then
    rm -f "$RUN_FILE.tmp"
    cat >&2 <<EOF
ERROR: sha256 mismatch for $ARCH
  expected: $EXPECTED
  actual:   $ACTUAL
  url:      $URL

The upstream artifact has changed since this SHA was pinned. Either:
  - Treat it as tampering and investigate.
  - If this is an intentional version bump, clear $SHA_VAR in
    $(basename "$ENV_FILE") and re-run this script. The new SHA will be
    captured and written back automatically.
EOF
    exit 1
  fi

  mv "$RUN_FILE.tmp" "$RUN_FILE"
fi

IMAGE_TAG="exasol-nano:${NANO_RELEASE_TAG}-${ARCH}"

echo "==> Building $IMAGE_TAG ($PLATFORM)"
podman build --platform "$PLATFORM" -t "$IMAGE_TAG" "$DIR"

OUT_DIR="$ROOT/output/nano-container"
mkdir -p "$OUT_DIR"

# Filename uses .tar.gz for compatibility with the launcher's metadata.json
# convention. The actual compression is zstd; podman load sniffs the magic
# bytes and doesn't rely on the extension.
TARBALL="$OUT_DIR/exasol-nano-${NANO_RELEASE_TAG}-${ARCH}.tar.gz"

echo "==> Saving image to $TARBALL"
podman save "$IMAGE_TAG" | zstd -19 -o "$TARBALL"
sha256sum "$TARBALL" | cut -d' ' -f1 > "$TARBALL.sha256"

echo "==> Done"
echo "    Image:   $IMAGE_TAG"
echo "    Tarball: $TARBALL"
echo "    SHA256:  $(cat "$TARBALL.sha256")"
