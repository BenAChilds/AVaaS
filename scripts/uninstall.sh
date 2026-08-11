#!/bin/sh
set -eu
[ "$(id -u)" -eq 0 ] || { echo "Run as root." >&2; exit 1; }
PURGE=false
[ "${1:-}" = --purge ] && PURGE=true
systemctl disable --now avaas.target 2>/dev/null || true
for unit in avaas-api.service avaas-worker.service avaas-clamd.service avaas-freshclam.service avaas.target; do
  rm -f "/etc/systemd/system/$unit"
done
rm -f /usr/local/bin/avaas
systemctl daemon-reload
if [ "$PURGE" = true ]; then
  printf 'Permanently remove AVaaS configuration, keys, definitions, logs, and spool data? [y/N] '
  read -r answer
  case "$answer" in y|Y|yes|YES) rm -rf /etc/avaas /var/lib/avaas /var/log/avaas /var/spool/avaas /run/avaas;; *) echo "Data preserved.";; esac
else
  echo "AVaaS removed; configuration and data were preserved. Use --purge for confirmed data removal."
fi
