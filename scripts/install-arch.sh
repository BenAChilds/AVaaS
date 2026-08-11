#!/bin/sh
set -eu
[ "$(id -u)" -eq 0 ] || { echo "Run as root." >&2; exit 1; }
. /etc/os-release
[ "${ID:-}" = arch ] || { echo "This installer supports Arch Linux." >&2; exit 1; }
pacman -Sy --needed --noconfirm ca-certificates clamav
SCRIPT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
exec "$SCRIPT_DIR/install-common.sh" "$@"

