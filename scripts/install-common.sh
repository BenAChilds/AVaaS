#!/bin/sh
set -eu

SCRIPT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
PROJECT_DIR=$(CDPATH= cd -- "$SCRIPT_DIR/.." && pwd)
BINARY="$PROJECT_DIR/avaas"
CONFIG="$PROJECT_DIR/configs/avaas.env"
ASSUME_YES=false
NO_START=false

while [ "$#" -gt 0 ]; do
  case "$1" in
    --binary) BINARY=$2; shift 2 ;;
    --config) CONFIG=$2; shift 2 ;;
    --yes) ASSUME_YES=true; shift ;;
    --no-start) NO_START=true; shift ;;
    *) echo "Unknown option: $1" >&2; exit 2 ;;
  esac
done

[ "$(id -u)" -eq 0 ] || { echo "This installer must run as root." >&2; exit 1; }
[ -x "$BINARY" ] || { echo "AVaaS binary not found or not executable: $BINARY" >&2; exit 1; }
[ -f "$CONFIG" ] || { echo "Configuration not found: $CONFIG" >&2; exit 1; }

if ! id avaas >/dev/null 2>&1; then
  useradd --system --home-dir /var/lib/avaas --shell /usr/sbin/nologin avaas 2>/dev/null || \
    useradd -r -d /var/lib/avaas -s /sbin/nologin avaas
fi
install -d -o avaas -g avaas -m 0750 /var/lib/avaas /var/lib/avaas/clamav /var/lib/avaas/clamav-tmp /var/spool/avaas /var/log/avaas /run/avaas
install -d -o root -g avaas -m 0750 /etc/avaas

install -m 0755 "$BINARY" /usr/local/bin/avaas.new
mv -f /usr/local/bin/avaas.new /usr/local/bin/avaas

install_config() {
  src=$1
  dst=$2
  mode=$3
  if [ -e "$dst" ]; then
    install -o root -g avaas -m "$mode" "$src" "$dst.new"
    echo "Preserved $dst; proposed update written to $dst.new"
  else
    install -o root -g avaas -m "$mode" "$src" "$dst"
  fi
}
install_config "$CONFIG" /etc/avaas/avaas.env 0640
install_config "$PROJECT_DIR/configs/clamd-native.conf" /etc/avaas/clamd.conf 0640
install_config "$PROJECT_DIR/configs/freshclam-native.conf" /etc/avaas/freshclam.conf 0640

make_secret() {
  path=$1
  if [ ! -s "$path" ]; then
    umask 077
    head -c 48 /dev/urandom | base64 > "$path"
    chown root:avaas "$path"
    chmod 0440 "$path"
  fi
}
make_secret /etc/avaas/api-key.pepper
make_secret /etc/avaas/url-encryption.key

for unit in avaas-api.service avaas-worker.service avaas-clamd.service avaas-freshclam.service avaas.target; do
  install -o root -g root -m 0644 "$PROJECT_DIR/systemd/$unit" "/etc/systemd/system/$unit"
done
systemctl daemon-reload

for unit in clamav-daemon clamav-freshclam clamd@scan clamd freshclam; do
  if systemctl is-active --quiet "$unit" 2>/dev/null; then
    if [ "$ASSUME_YES" != true ]; then
      printf 'Disable conflicting service %s? [y/N] ' "$unit"
      read -r answer
      case "$answer" in y|Y|yes|YES) ;; *) echo "Resolve the service conflict and rerun." >&2; exit 1;; esac
    fi
    systemctl disable --now "$unit"
  fi
done

su -s /bin/sh avaas -c '/usr/local/bin/avaas migrate'
if [ ! -s /var/lib/avaas/clamav/main.cvd ] && [ ! -s /var/lib/avaas/clamav/main.cld ]; then
  su -s /bin/sh avaas -c '/usr/bin/freshclam --config-file=/etc/avaas/freshclam.conf'
fi

systemctl enable avaas.target
if [ "$NO_START" != true ]; then
  systemctl restart avaas.target
  sleep 2
  /usr/local/bin/avaas doctor || true
fi
echo "AVaaS installed. Create the first key with: sudo -u avaas /usr/local/bin/avaas keygen --description \"first administrator\""
