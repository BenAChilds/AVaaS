# AVaaS (Anti-Virus as a Service)

AVaaS turns [ClamAV](https://www.clamav.net/) into a small authenticated, asynchronous HTTP service. Submit a public HTTP(S) URL, receive a status URL immediately, and poll until the file is reported clean, infected, or failed.

> [!IMPORTANT]
> ClamAV is the main resource consumer. Allocate at least 6–8 GiB RAM to Docker Desktop and use an 8 GiB Linode for production. AVaaS is one layer in a security programme, not a substitute for access control, sandboxing, patching, or content validation.

## Features

- Durable SQLite queue with leases, retries, restart recovery, and idempotent submissions.
- Constant-memory downloads up to 2 GiB and strict SSRF protection.
- Scoped, described, revocable API keys whose secrets are shown once.
- Automatic FreshClam definition updates and clamd reloads.
- Queue, ClamAV version, statistics, health, and sanitized log endpoints.
- Optional Valkey Pub/Sub lifecycle events to any nominated host and topic.
- Docker Compose for Linux, macOS, and Windows.
- Native systemd installers for Debian/Ubuntu, RHEL-compatible distributions, and Arch.

```text
Client → Caddy/TLS → AVaaS API → SQLite queue → AVaaS worker → clamd
                                      ↓
                            optional Valkey Pub/Sub
```

## Five-minute Docker quick start

Requirements: Docker Engine with Compose v2 (or Docker Desktop) and at least 6 GiB available RAM.
On Apple Silicon, Docker Desktop runs the official amd64 ClamAV image under emulation; AVaaS itself is multi-architecture.

```sh
git clone https://github.com/benachilds/avaas.git
cd avaas
cp .env.example .env
docker compose up -d --build
```

The first startup downloads ClamAV definitions and can take several minutes. Wait until the stack is healthy, then create an API key:

```sh
docker compose exec api avaas keygen --description "local development"
```

Save the displayed token; it cannot be recovered. Submit a file:

```sh
export AVAAS_TOKEN='avaas_ID.SECRET'
curl -i -X POST http://localhost:8080/v1/scans \
  -H "Authorization: Bearer $AVAAS_TOKEN" \
  -H 'Content-Type: application/json' \
  -H 'Idempotency-Key: example-001' \
  --data '{"url":"https://example.org/file.pdf"}'
```

Poll the returned `status_url`:

```sh
curl -H "Authorization: Bearer $AVAAS_TOKEN" \
  http://localhost:8080/v1/scans/JOB_ID
```

A completed clean result resembles:

```json
{
  "id": "83af...",
  "status": "completed",
  "verdict": "clean",
  "attempts": 1,
  "max_attempts": 3,
  "bytes": 48211,
  "download_ms": 91,
  "scan_ms": 18,
  "created_at": "2026-08-11T01:00:00Z",
  "completed_at": "2026-08-11T01:00:01Z"
}
```

An infected result has `"verdict":"infected"` and a `signature`. A terminal operational failure has `"verdict":"error"` and a stable `error_code`. Retrying work reports `"status":"retrying"`.

Stop the stack with `docker compose down`. Named volumes intentionally preserve keys, jobs, definitions, and logs. Add `--volumes` only when you intend to erase them.

## API

The canonical contract is [openapi.yaml](openapi.yaml).

| Method | Path                | Access        | Purpose                         |
| ------ | ------------------- | ------------- | ------------------------------- |
| `POST` | `/v1/scans`         | `scan`        | Submit an HTTP(S) URL           |
| `GET`  | `/v1/scans/{id}`    | owner/admin   | Poll a scan                     |
| `GET`  | `/v1/queue`         | `admin`       | Queue counts                    |
| `GET`  | `/v1/system/clamav` | authenticated | Engine/definition version       |
| `POST` | `/v1/system/clamav/definitions/update` | `admin` | Request a definition update check |
| `GET`  | `/v1/stats`         | `admin`       | Lifetime and 24-hour statistics |
| `GET`  | `/v1/logs`          | authenticated | Sanitized bounded logs          |
| `GET`  | `/health/live`      | public        | Process liveness                |
| `GET`  | `/health/ready`     | public        | Dependency readiness            |

Examples:

```sh
curl -H "Authorization: Bearer $AVAAS_TOKEN" http://localhost:8080/v1/queue
curl -H "Authorization: Bearer $AVAAS_TOKEN" http://localhost:8080/v1/system/clamav
curl -X POST -H "Authorization: Bearer $AVAAS_TOKEN" http://localhost:8080/v1/system/clamav/definitions/update
curl -H "Authorization: Bearer $AVAAS_TOKEN" http://localhost:8080/v1/stats
curl -G -H "Authorization: Bearer $AVAAS_TOKEN" http://localhost:8080/v1/logs \
  --data-urlencode source=clamav --data-urlencode limit=50
curl -G -H "Authorization: Bearer $AVAAS_TOKEN" http://localhost:8080/v1/logs \
  --data-urlencode source=api --data-urlencode level=ERROR
curl -G -H "Authorization: Bearer $AVAAS_TOKEN" http://localhost:8080/v1/logs \
  --data-urlencode source=worker --data-urlencode limit=100
```

Log queries accept `source=api|worker|clamav|freshclam|all`, `level`, RFC3339 `since`/`until`, `limit` (maximum 500), and an opaque `cursor`. Only the previous seven days are queryable. Records are normalized and aggressively sanitized; raw paths, URLs, secrets, key IDs, and authorization data are never returned. All valid, non-revoked keys may use this endpoint.

Definition update requests are asynchronous and require an administrator key. A request wakes the existing FreshClam daemon; repeated requests are coalesced and limited to one wake-up every 15 minutes. Observe completion through `/v1/system/clamav` and `source=freshclam` logs. Scheduled FreshClam checks continue independently if the relay is unavailable.

## API keys

Keys use a public ID plus a 256-bit secret. AVaaS stores only a peppered digest. The pepper file must be backed up separately from SQLite; losing it invalidates every key.

```sh
avaas keygen --description "production S3 uploader" --scopes scan
avaas keys list
avaas keys list --include-revoked --json
avaas keys revoke KEY_ID --reason "integration retired"
```

`scan` permits submission. `admin` implies all access and permits global queue/statistics and cross-owner job reads. Revocation is checked on every request. The CLI refuses to revoke the final administrator unless `--yes` is supplied.

Reset aggregate statistics while preserving jobs and API keys with:

```sh
avaas stats reset --yes
avaas stats reset --yes --json
```

This deletes hourly aggregate buckets and notification publication history. The explicit `--yes` guard is always required. With Compose, prefix the command with `docker compose exec api`.

With Compose, prefix commands with `docker compose exec api`. The token is printed exactly once; descriptions are metadata and must not contain secrets.

## Configuration

AVaaS reads environment variables and optionally a `KEY=VALUE` file supplied as `avaas --config FILE COMMAND`.

| Variable                    | Default                         | Meaning                               |
| --------------------------- | ------------------------------- | ------------------------------------- |
| `AVAAS_LISTEN_ADDRESS`      | `:8080`                         | API bind address                      |
| `AVAAS_PUBLIC_BASE_URL`     | `http://localhost:8080`         | Base for returned status URLs         |
| `AVAAS_DATABASE_PATH`       | `/var/lib/avaas/avaas.db`       | Local SQLite database; do not use NFS |
| `AVAAS_PEPPER_FILE`         | `/etc/avaas/api-key.pepper`     | API-key digest pepper                 |
| `AVAAS_ENCRYPTION_KEY_FILE` | `/etc/avaas/url-encryption.key` | Queued-URL encryption secret          |
| `AVAAS_SPOOL_DIR`           | `/var/spool/avaas`              | Temporary downloads                   |
| `AVAAS_LOG_DIR`             | `/var/log/avaas`                | Rotated API/worker/ClamAV logs        |
| `AVAAS_CLAMD_SOCKET`        | `/run/clamav/clamd.sock`        | Docker socket; native installs use `/run/avaas/clamd.sock` |
| `AVAAS_DEFINITION_UPDATE_TRIGGER` | `/run/avaas/definition-update.request` | Optional definition-update request marker |
| `AVAAS_DEFINITION_UPDATE_MIN_INTERVAL` | `15m` | Minimum interval between manual wake-ups |
| `AVAAS_FRESHCLAM_PID_FILE` | `/run/avaas/freshclam.pid` | FreshClam PID used by the relay |
| `AVAAS_MAX_FILE_BYTES`      | `2147483648`                    | Hard download limit                   |
| `AVAAS_QUEUE_LIMIT`         | `1000`                          | Outstanding-job ceiling               |
| `AVAAS_WORKER_CONCURRENCY`  | `1`                             | Simultaneous downloads/scans          |
| `AVAAS_MAX_ATTEMPTS`        | `3`                             | Initial attempt plus two retries      |
| `AVAAS_RESULT_RETENTION`    | `24h`                           | Completed result retention            |
| `AVAAS_DOWNLOAD_TIMEOUT`    | `2h`                            | Per-attempt download/scan deadline    |
| `AVAAS_DOWNLOAD_ALLOWED_PRIVATE_HOSTS` | empty                 | Development-only exact hosts allowed to resolve privately |

Concurrency multiplies spool and ClamAV load. Provision at least `concurrency × 2 GiB` of spool space plus ClamAV extraction headroom.

Compose also mounts the socket at the legacy `/run/avaas/clamd.sock` path so existing `.env` files continue to work. New installations should use `/run/clamav/clamd.sock`, which matches the official ClamAV container entrypoint.

### Local Valet development

Private destinations remain blocked by default. `AVAAS_DOWNLOAD_ALLOWED_PRIVATE_HOSTS` is an explicit development exception for applications served from a trusted private address. It is read by the worker, accepts a comma-separated list of exact hostnames or IP literals, and does not support schemes, ports, paths, or wildcards.

For a Laravel Valet site running on the macOS Docker host, add a `compose.override.yaml` that maps the exact Valet hostname to Docker's host gateway, explicitly allows that hostname, and mounts only Valet's public CA certificate:

```yaml
services:
  worker:
    extra_hosts:
      - "simskies.test:host-gateway"
    environment:
      AVAAS_DOWNLOAD_ALLOWED_PRIVATE_HOSTS: simskies.test
      SSL_CERT_FILE: /etc/avaas-dev/valet-ca.pem
    volumes:
      - /Users/you/.config/valet/CA/LaravelValetCASelfSigned.pem:/etc/avaas-dev/valet-ca.pem:ro
```

Only the worker needs these settings. Rebuild and recreate it, then confirm the effective configuration and hostname resolution:

```sh
docker compose up -d --build --force-recreate worker
docker compose exec worker printenv AVAAS_DOWNLOAD_ALLOWED_PRIVATE_HOSTS
docker compose exec worker getent hosts simskies.test
```

Valet's CA file may use `.pem` or `.crt`; the contents must be a PEM-encoded public certificate. Never mount `LaravelValetCASelfSigned.key` or another private key. `SSL_CERT_FILE` allows Go's HTTPS client to trust Valet without disabling certificate verification.

Jobs that already completed with `blocked_destination` are terminal and are not reopened when the worker restarts. Submit a new job after changing the configuration and use a new `Idempotency-Key`; reusing the old key returns the original completed job.

The exception permits only private unicast addresses for exact configured hosts. Loopback, link-local/cloud-metadata, carrier-grade NAT, benchmarking, documentation, multicast, unspecified, and reserved destinations remain blocked even if their hostname is listed. DNS is resolved and the address policy is reapplied for every connection and redirect, preventing an allowed hostname from rebinding or redirecting to a hard-blocked destination. Keep the allowlist empty in production unless private retrieval is an intentional, reviewed requirement.

### Optional Valkey notifications

Valkey is not the queue and is never required to scan. SQLite records state first, then a bounded transactional outbox publishes `scan.queued`, `scan.started`, `scan.retrying`, and `scan.completed` events.

```env
AVAAS_VALKEY_ENABLED=true
AVAAS_VALKEY_HOST=valkey.example.internal
AVAAS_VALKEY_PORT=6379
AVAAS_VALKEY_USERNAME=avaas
AVAAS_VALKEY_PASSWORD_FILE=/run/secrets/valkey-password
AVAAS_VALKEY_TLS=true
AVAAS_VALKEY_TLS_SERVER_NAME=valkey.example.internal
AVAAS_VALKEY_TOPIC=security.avaas.events
```

For the local optional container:

```sh
cat >> .env <<'EOF'
AVAAS_VALKEY_ENABLED=true
AVAAS_VALKEY_HOST=valkey
EOF
docker compose --profile valkey up -d
```

Events contain `schema_version`, `event_id`, `event_type`, `job_id`, `occurred_at`, and safe result data. Pub/Sub is not durable: a disconnected subscriber can miss an already-published event. Consumers must deduplicate possible retries using `event_id`. Valkey connections are established lazily and reset after publication failures; an outage never prevents the worker from starting or blocks scanning, and pending events remain in the bounded SQLite outbox for retry.

AVaaS disables Valkey client-side caching and client metadata commands. A dedicated ACL user therefore does not need `CLIENT TRACKING` or other `CLIENT` permissions. Grant it authentication/connectivity, `PING`, `PUBLISH`, `SELECT` only when a nonzero database is configured, and publish access only to the configured topic. Subscribers need their own subscribe permission and channel access. Keep the password in the file named by `AVAAS_VALKEY_PASSWORD_FILE`, outside `/root` and readable by the `avaas` group; native installations commonly use `/etc/avaas/valkey.password` with ownership `root:avaas` and mode `0440`.

## Production on Linode

Use an Ubuntu LTS Linode with at least 2 vCPU, 8 GiB RAM, and 40 GiB disk. Point `AVAAS_DOMAIN` to the instance, permit only SSH/80/443 in Cloud Firewall and UFW, then:

```sh
cp .env.example .env
printf '\nAVAAS_DOMAIN=scan.example.com\n' >> .env
docker compose -f compose.yaml -f compose.prod.yaml up -d --build
```

Caddy obtains and renews TLS certificates. SQLite, clamd, and Valkey have no public ports. Review logs and `/health/ready`, back up the SQLite database and both secret files, and test restoration. Use SQLite's online backup command/API rather than copying a live WAL database. Upgrade with `docker compose pull`, rebuild AVaaS, and recreate the stack during a maintenance window. FreshClam updates definitions independently several times daily.

## Native Linux installation

Download and verify a release archive, extract it, then run the matching installer:

```sh
sudo ./scripts/install-debian.sh --binary ./avaas --yes
sudo ./scripts/install-rhel.sh --binary ./avaas --yes
sudo ./scripts/install-arch.sh --binary ./avaas --yes
```

The scripts install ClamAV/FreshClam, a locked `avaas` user, configuration, secrets, hardened `avaas-*.service` units, and `avaas.target`. When distro ClamAV AppArmor profiles are present, the installer adds narrowly scoped local rules for AVaaS configuration, definitions, logs, sockets, temporary extraction, and spool paths without disabling confinement. Existing configuration is preserved and proposed replacements use a `.new` suffix. Use `--no-start` to stage installation. RHEL may enable EPEL after confirmation (or with `--yes`).

Native installation binds to `127.0.0.1:8080` by default. For remote production access, keep that private bind, set `AVAAS_PUBLIC_BASE_URL` to the HTTPS origin, and place Caddy or another maintained TLS reverse proxy in front of it. Never expose the native HTTP listener, clamd socket, or SQLite file directly to the Internet.

Check the installation with `sudo -u avaas avaas doctor`, manage it with `systemctl status avaas.target`, and uninstall without deleting data using `sudo ./scripts/uninstall.sh`. `--purge` requires an additional destructive confirmation.

## Upgrading

Read the release notes and `CHANGELOG.md` before upgrading. Verify the downloaded archive against the published `checksums.txt`, retain the previous release until verification succeeds, and schedule a maintenance window. Back up SQLite together with both secret files: a database backup without its matching API-key pepper and URL-encryption key is not a usable recovery set.

### Docker Compose

Stop the API and worker before copying SQLite so no process retains a WAL connection. ClamAV and Caddy may remain running:

```sh
install -d -m 0700 backups/pre-upgrade
docker compose stop api worker
docker compose cp api:/var/lib/avaas/avaas.db backups/pre-upgrade/avaas.db
docker compose cp api:/etc/avaas-secrets/api-key.pepper backups/pre-upgrade/api-key.pepper
docker compose cp api:/etc/avaas-secrets/url-encryption.key backups/pre-upgrade/url-encryption.key
chmod 0600 backups/pre-upgrade/*
```

Update the checked-out release, review changes to `.env.example` and the Compose files, preserve the existing `.env`, then rebuild and recreate the stack:

```sh
git pull --ff-only
docker compose -f compose.yaml -f compose.prod.yaml build --pull api worker init
docker compose -f compose.yaml -f compose.prod.yaml up -d --remove-orphans
docker compose -f compose.yaml -f compose.prod.yaml exec api avaas version
curl --fail --silent https://scan.example.com/health/ready
```

Add `--profile valkey` to `up` when using the bundled optional Valkey service. If the upgrade fails before recreation completes, run `docker compose start api worker` to resume the old containers. After new containers have migrated SQLite, rollback requires restoring the pre-upgrade database and matching secrets as a set, then recreating containers from the previous image or source revision. Never restore a live database underneath running API or worker containers.

### Native Linux

Download and verify the new release archive first. Stop AVaaS, then take a root-readable backup of the binary, configuration and secrets, SQLite database, and systemd units:

```sh
upgrade_backup=/root/avaas-pre-upgrade
install -d -m 0700 "$upgrade_backup"
systemctl stop avaas.target
cp -a /usr/local/bin/avaas "$upgrade_backup/"
cp -a /etc/avaas "$upgrade_backup/"
cp -a /var/lib/avaas/avaas.db "$upgrade_backup/"
cp -a /etc/systemd/system/avaas-*.service /etc/systemd/system/avaas.target "$upgrade_backup/"
```

Run the installer from the newly extracted release for the host distribution. It replaces the binary and units atomically, applies migrations, preserves active configuration, and writes proposed configuration changes with a `.new` suffix:

```sh
sudo ./scripts/install-debian.sh --binary ./avaas --yes
# Or install-rhel.sh / install-arch.sh as appropriate.

diff -u /etc/avaas/avaas.env /etc/avaas/avaas.env.new || true
sudo -u avaas /usr/local/bin/avaas --config /etc/avaas/avaas.env version
sudo -u avaas /usr/local/bin/avaas --config /etc/avaas/avaas.env doctor
systemctl status avaas.target --no-pager
```

Review and merge `.new` files deliberately; do not overwrite secrets or working configuration wholesale. Restart affected services after merging. For rollback, stop `avaas.target`, restore the binary, database, `/etc/avaas`, and units from the same pre-upgrade backup, run `systemctl daemon-reload`, and start the target. Restoring only the old binary after a schema migration is unsupported.

## Security model and limitations

- Only HTTP(S) URLs are accepted. Userinfo is rejected.
- DNS is resolved for each connection and redirect. Private unicast destinations require an exact opt-in hostname; loopback, link-local, reserved, multicast, and metadata destinations cannot be overridden.
- Redirects, response headers, total duration, queue size, and downloaded bytes are bounded.
- Presigned URLs are encrypted in SQLite and omitted from logs/results/events.
- Downloaded files use generated names, are scanned over a private Unix socket, and are deleted rather than quarantined.
- ClamAV parser/archive limits are configured. A limit-exceeded scan is never considered clean.
- v1 is single-host. SQLite on a network filesystem and multi-node workers are unsupported.
- v1 does not provide uploads, `s3://`, callbacks, quarantine, permanent history, or a web UI.

## Development

```sh
go test ./...
go test -race ./...
go vet ./...
go build ./cmd/avaas
docker compose config
```

Run `gofmt`, `staticcheck`, `golangci-lint`, and `govulncheck` before a pull request. See [CONTRIBUTING.md](CONTRIBUTING.md) and report vulnerabilities privately as described in [SECURITY.md](SECURITY.md).

## Troubleshooting

- **Startup remains unhealthy:** the first definition download is large; inspect `docker compose logs clamav`.
- **A native installation initially has no clamd socket:** after the first definition download, clamd must load several million signatures before creating `/run/avaas/clamd.sock`. Current installers wait up to three minutes and print service diagnostics if startup actually fails.
- **Native FreshClam says it cannot open/parse `/etc/avaas/freshclam.conf`:** compare the preserved file with `/etc/avaas/freshclam.conf.new`, then check `journalctl -k` for AppArmor denials. Current installers add the required local AppArmor rules automatically.
- **`database is locked`:** keep the database on a local Docker volume/filesystem and do not run unsupported multi-host workers.
- **Downloads are blocked:** the resolved address is private/reserved or a redirect entered a blocked network.
- **An allowed development host is still blocked:** rebuild and recreate the worker, confirm the exact hostname is present in `AVAAS_DOWNLOAD_ALLOWED_PRIVATE_HOSTS`, and use a new idempotency key. A hostname resolving to loopback, metadata/link-local, or another hard-blocked range is never permitted.
- **A Valet download reports a temporary TLS failure:** mount Valet's public CA certificate into the worker and point `SSL_CERT_FILE` at it; do not mount the CA private key or disable TLS verification globally.
- **Daily memory spike:** ensure `ConcurrentDatabaseReload no` is active and allocate 8 GiB RAM.
- **Valkey events absent:** check `/v1/stats`; scans continue while the publisher retries its outbox.
- **A key was lost:** create a new key; plaintext keys cannot be recovered.

## License

AVaaS is licensed under the [GNU General Public License v3.0](LICENSE). ClamAV, dependencies, and container images retain their respective licenses.
