#!/bin/sh
# Tavazon one-line installer (Linux + systemd).
#
#   curl -fsSL https://raw.githubusercontent.com/salehi/tavazon/main/install.sh | sh
#
# It downloads the matching release binary, lays out a self-contained install
# directory (binary + config + data/ + maxmind_files/), installs the systemd
# unit, and enables + starts the service. It warns if the GeoLite2 .mmdb
# databases are missing — those are operator-supplied and never bundled;
# Tavazon runs without them but the uploader stays idle until they are provided.
#
# The service is system-wide, so root is required; the script uses sudo as
# needed.
#
# Overrides:
#   curl ... | TAVAZON_DIR=/srv/tavazon sh    # install dir (default /opt/tavazon)
#   curl ... | sh -s -- 1.0.0                 # pin a version (default: latest release)
set -eu

REPO="salehi/tavazon"
DIR="${TAVAZON_DIR:-/opt/tavazon}"
VERSION="${1:-${VERSION:-}}"

die() { echo "error: $*" >&2; exit 1; }

command -v curl >/dev/null 2>&1 || die "curl is required"
command -v tar  >/dev/null 2>&1 || die "tar is required"
command -v systemctl >/dev/null 2>&1 \
  || die "systemd (systemctl) not found — this installer manages tavazon as a systemd service"

# --- detect platform, mapped to release asset names ---
case "$(uname -s)" in
  Linux) OS=linux ;;
  *) die "this installer targets Linux + systemd; on $(uname -s) grab a build from https://github.com/${REPO}/releases" ;;
esac
case "$(uname -m)" in
  x86_64|amd64)      ARCH=x86_64 ;;
  aarch64|arm64)     ARCH=arm64 ;;
  armv7l|armv6l|arm) ARCH=armv7 ;;
  *) die "unsupported architecture '$(uname -m)'" ;;
esac

# --- root is needed for the system-wide install and service management ---
SUDO=""
[ "$(id -u)" -eq 0 ] || SUDO="sudo"

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

echo "Installing tavazon ${VERSION} (${OS}/${ARCH}) into ${DIR}"

# --- download & extract ---
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
curl -fSL "$URL" -o "$tmp/pkg.tar.gz" || die "download failed: $URL"
tar -xzf "$tmp/pkg.tar.gz" -C "$tmp"
src="$tmp/tavazon-${VERSION}-${OS}-${ARCH}"

# --- install files (never clobber an existing config.json) ---
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
if [ ! -f "$DIR/maxmind_files/GeoLite2-ASN.mmdb" ] || [ ! -f "$DIR/maxmind_files/GeoLite2-Country.mmdb" ]; then
  echo
  echo "WARNING: GeoLite2 databases not found in $DIR/maxmind_files/"
  echo "         expected: GeoLite2-ASN.mmdb and GeoLite2-Country.mmdb"
  echo "         Tavazon runs without them but the uploader stays idle until they"
  echo "         are provided. Get them free: https://www.maxmind.com/en/geolite2/signup"
fi

# --- install + start the systemd service, with its paths pointed at $DIR ---
unit_src="$src/tavazon.service"
if [ ! -f "$unit_src" ]; then
  # older archives didn't bundle the unit; fall back to the repo copy
  unit_src="$tmp/tavazon.service"
  curl -fsSL "https://raw.githubusercontent.com/${REPO}/main/systemd/tavazon.service" \
    -o "$unit_src" || die "could not obtain the systemd unit file"
fi
sed "s#/opt/tavazon#${DIR}#g" "$unit_src" | $SUDO tee /etc/systemd/system/tavazon.service >/dev/null
$SUDO systemctl daemon-reload
$SUDO systemctl enable --now tavazon \
  || die "unit installed but the service failed to start — inspect: ${SUDO:+sudo }journalctl -u tavazon -n 50"

echo
echo "tavazon is installed and running as a systemd service."
echo "  status:  ${SUDO:+sudo }systemctl status tavazon"
echo "  logs:    ${SUDO:+sudo }journalctl -u tavazon -f"
echo "  config:  $DIR/config.json   (run '${SUDO:+sudo }systemctl restart tavazon' after edits)"
