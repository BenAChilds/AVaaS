#!/bin/sh
set -eu
[ "$(id -u)" -eq 0 ] || { echo "Run as root." >&2; exit 1; }
# Provided by supported RHEL-family systems.
# shellcheck disable=SC1091
. /etc/os-release
case "${ID_LIKE:-}${ID:-}" in *rhel*|*fedora*|*centos*) ;; *) echo "This installer supports RHEL-compatible systems." >&2; exit 1;; esac
if ! dnf -q list available clamav >/dev/null 2>&1 && ! rpm -q epel-release >/dev/null 2>&1; then
  case " $* " in *" --yes "*) ;; *) printf 'Enable EPEL to install ClamAV? [y/N] '; read -r answer; case "$answer" in y|Y|yes|YES) ;; *) exit 1;; esac;; esac
  dnf install -y epel-release
fi
dnf install -y ca-certificates clamav clamav-update clamd
SCRIPT_DIR=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)
exec "$SCRIPT_DIR/install-common.sh" "$@"
