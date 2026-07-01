#!/usr/bin/env sh
# Orcinus installer. Downloads the latest release binary for your OS/arch.
#
#   curl -fsSL https://raw.githubusercontent.com/orcinustools/orcinus/main/install.sh | sh
#
# Environment overrides:
#   ORCINUS_VERSION   release tag to install (default: latest)
#   ORCINUS_INSTALL   install directory       (default: /usr/local/bin)
set -eu

REPO="orcinustools/orcinus"
INSTALL_DIR="${ORCINUS_INSTALL:-/usr/local/bin}"
VERSION="${ORCINUS_VERSION:-latest}"

say() { printf 'orcinus-install: %s\n' "$1" >&2; }
die() { say "$1"; exit 1; }

# Detect OS.
os="$(uname -s | tr '[:upper:]' '[:lower:]')"
case "$os" in
  linux) os="linux" ;;
  darwin) os="darwin" ;;
  *) die "unsupported OS: $os (linux and darwin only)" ;;
esac

# Detect arch.
arch="$(uname -m)"
case "$arch" in
  x86_64 | amd64) arch="amd64" ;;
  aarch64 | arm64) arch="arm64" ;;
  *) die "unsupported architecture: $arch" ;;
esac

# Resolve the version tag.
if [ "$VERSION" = "latest" ]; then
  VERSION="$(curl -fsSL "https://api.github.com/repos/${REPO}/releases/latest" \
    | grep '"tag_name"' | head -1 | cut -d '"' -f 4)"
  [ -n "$VERSION" ] || die "could not resolve the latest release tag"
fi

# goreleaser strips the leading 'v' from the version in archive names.
ver_no_v="${VERSION#v}"
asset="orcinus_${ver_no_v}_${os}_${arch}.tar.gz"
url="https://github.com/${REPO}/releases/download/${VERSION}/${asset}"

say "downloading ${asset} (${VERSION})"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
curl -fsSL "$url" -o "$tmp/orcinus.tar.gz" || die "download failed: $url"
tar -xzf "$tmp/orcinus.tar.gz" -C "$tmp"

# Install (uses sudo if the target dir is not writable).
if [ -w "$INSTALL_DIR" ]; then
  install -m 0755 "$tmp/orcinus" "$INSTALL_DIR/orcinus"
else
  say "elevating with sudo to write $INSTALL_DIR"
  sudo install -m 0755 "$tmp/orcinus" "$INSTALL_DIR/orcinus"
fi

say "installed orcinus ${VERSION} to ${INSTALL_DIR}/orcinus"
"$INSTALL_DIR/orcinus" version || true
