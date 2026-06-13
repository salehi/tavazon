#!/bin/sh
# Tavazon one-line installer (Linux and macOS).
#
#   curl -fsSL https://raw.githubusercontent.com/salehi/tavazon/main/install.sh | sh
#
# It downloads the matching release binary, lays out a self-contained install
# directory (binary + config + data/ + maxmind_files/), and warns if the
# GeoLite2 .mmdb databases are missing — those are operator-supplied and never
# bundled. It installs THIS project only; it does not fetch the databases for
# you, nor install a systemd unit.
#
# Overrides:
#   TAVAZON_DIR=~/tavazon  curl ... | TAVAZON_DIR=~/tavazon sh     # install dir (default /opt/tavazon)
#   curl ... | sh -s -- 1.0.0                                      # pin a version (default: latest release)
set -eu

REPO="salehi/tavazon"
DIR="${TAVAZON_DIR:-/opt/tavazon}"
VERSION="${1:-${VERSION:-}}"

die() { echo "error: $*" >&2; exit 1; }

command -v curl >/dev/null 2>&1 || die "curl is required"
command -v tar  >/dev/null 2>&1 || die "tar is required"

# --- detect platform, mapped to release asset names ---
case "$(uname -s)" in
  Linux)  OS=linux ;;
  Darwin) OS=macos ;;
  *) die "unsupported OS '$(uname -s)' — Windows users download from the Releases page" ;;
esac
case "$(uname -m)" in
  x86_64|amd64)      ARCH=x86_64 ;;
  aarch64|arm64)     ARCH=arm64 ;;
  armv7l|armv6l|arm) ARCH=armv7 ;;
  *) die "unsupported architecture '$(uname -m)'" ;;
esac

# --- resolve version -> release tag ---
if [ -n "$VERSION" ]; then
  TAG="release-${VERSION}"
else
  TAG=$(curl -fsSL "https://api.github.com/repos/${REPO}/releases/latest" \
        | grep '"tag_name"' | head -1 | cut -d'"' -f4)
  [ -n "$TAG" ] || die "could not determine the latest release (set a version: sh -s -- 1.0.0)"
  VERSION="${TAG#release-}"
fi

ASSET="tavazon-${VERSION}-${OS}-${ARCH}.tar.gz"
URL="https://github.com/${REPO}/releases/download/${TAG}/${ASSET}"

# --- use sudo only when the target is not writable ---
SUDO=""
if [ -e "$DIR" ]; then
  [ -w "$DIR" ] || SUDO="sudo"
else
  [ -w "$(dirname "$DIR")" ] || SUDO="sudo"
fi

echo "Installing tavazon ${VERSION} (${OS}/${ARCH}) into ${DIR}"

# --- download & extract ---
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
curl -fSL "$URL" -o "$tmp/pkg.tar.gz" || die "download failed: $URL"
tar -xzf "$tmp/pkg.tar.gz" -C "$tmp"
src="$tmp/tavazon-${VERSION}-${OS}-${ARCH}"

# --- install (never clobber an existing config.json) ---
$SUDO mkdir -p "$DIR/maxmind_files" "$DIR/data"
$SUDO cp "$src/tavazon" "$DIR/tavazon"
$SUDO chmod +x "$DIR/tavazon"
$SUDO cp "$src/config.example.json" "$DIR/config.example.json"
if [ -f "$DIR/config.json" ]; then
  echo "Kept existing $DIR/config.json"
else
  $SUDO cp "$src/config.example.json" "$DIR/config.json"
fi

# --- warn if the operator-supplied GeoLite2 databases are missing ---
asn="$DIR/maxmind_files/GeoLite2-ASN.mmdb"
country="$DIR/maxmind_files/GeoLite2-Country.mmdb"
if [ ! -f "$asn" ] || [ ! -f "$country" ]; then
  echo
  echo "WARNING: GeoLite2 databases not found in $DIR/maxmind_files/"
  echo "         expected: GeoLite2-ASN.mmdb and GeoLite2-Country.mmdb"
  echo "         Tavazon starts without them but the uploader stays idle until they"
  echo "         are provided. Get them free: https://www.maxmind.com/en/geolite2/signup"
fi

echo
echo "Installed $DIR/tavazon"
echo "Run it:   cd $DIR && ./tavazon -config config.json"
