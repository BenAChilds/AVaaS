#!/bin/sh
set -eu
[ "$(id -u)" -eq 0 ] || { echo "Run as root." >&2; exit 1; }
. /etc/os-release
case "${ID:-}" in debian|ubuntu) ;; *) echo "This installer supports Debian and Ubuntu." >&2; exit 1;; esac
apt-get update
DEBIAN_FRONTEND=noninteractive apt-get install -y ca-certificates clamav clamav-daemon clamav-freshclam
SCRIPT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
exec "$SCRIPT_DIR/install-common.sh" "$@"

