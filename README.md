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
| `GET`  | `/v1/stats`         | `admin`       | Lifetime and 24-hour statistics |
| `GET`  | `/v1/logs`          | authenticated | Sanitized bounded logs          |
| `GET`  | `/health/live`      | public        | Process liveness                |
| `GET`  | `/health/ready`     | public        | Dependency readiness            |

Examples:

```sh
curl -H "Authorization: Bearer $AVAAS_TOKEN" http://localhost:8080/v1/queue
curl -H "Authorization: Bearer $AVAAS_TOKEN" http://localhost:8080/v1/system/clamav
curl -H "Authorization: Bearer $AVAAS_TOKEN" http://localhost:8080/v1/stats
curl -G -H "Authorization: Bearer $AVAAS_TOKEN" http://localhost:8080/v1/logs \
  --data-urlencode source=clamav --data-urlencode limit=50
```

Log queries accept `source=api|worker|clamav|freshclam|all`, `level`, RFC3339 `since`/`until`, `limit` (maximum 500), and an opaque `cursor`. Only the previous seven days are queryable. Records are normalized and aggressively sanitized; raw paths, URLs, secrets, key IDs, and authorization data are never returned. All valid, non-revoked keys may use this endpoint.

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
| `AVAAS_MAX_FILE_BYTES`      | `2147483648`                    | Hard download limit                   |
| `AVAAS_QUEUE_LIMIT`         | `1000`                          | Outstanding-job ceiling               |
| `AVAAS_WORKER_CONCURRENCY`  | `1`                             | Simultaneous downloads/scans          |
| `AVAAS_MAX_ATTEMPTS`        | `3`                             | Initial attempt plus two retries      |
| `AVAAS_RESULT_RETENTION`    | `24h`                           | Completed result retention            |
| `AVAAS_DOWNLOAD_TIMEOUT`    | `2h`                            | Per-attempt download/scan deadline    |

Concurrency multiplies spool and ClamAV load. Provision at least `concurrency × 2 GiB` of spool space plus ClamAV extraction headroom.

Compose also mounts the socket at the legacy `/run/avaas/clamd.sock` path so existing `.env` files continue to work. New installations should use `/run/clamav/clamd.sock`, which matches the official ClamAV container entrypoint.

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

Events contain `schema_version`, `event_id`, `event_type`, `job_id`, `occurred_at`, and safe result data. Pub/Sub is not durable: a disconnected subscriber can miss an already-published event. Consumers must deduplicate possible retries using `event_id`. Valkey failure never blocks scanning.

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

The scripts install ClamAV/FreshClam, a locked `avaas` user, configuration, secrets, hardened `avaas-*.service` units, and `avaas.target`. Existing configuration is preserved and proposed replacements use a `.new` suffix. Use `--no-start` to stage installation. RHEL may enable EPEL after confirmation (or with `--yes`).

Native installation binds to `127.0.0.1:8080` by default. For remote production access, keep that private bind, set `AVAAS_PUBLIC_BASE_URL` to the HTTPS origin, and place Caddy or another maintained TLS reverse proxy in front of it. Never expose the native HTTP listener, clamd socket, or SQLite file directly to the Internet.

Check the installation with `sudo -u avaas avaas doctor`, manage it with `systemctl status avaas.target`, and uninstall without deleting data using `sudo ./scripts/uninstall.sh`. `--purge` requires an additional destructive confirmation.

## Security model and limitations

- Only HTTP(S) URLs are accepted. Userinfo is rejected.
- DNS is resolved for each connection and redirect; loopback, private, link-local, reserved, container, multicast, and metadata destinations are blocked.
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
- **`database is locked`:** keep the database on a local Docker volume/filesystem and do not run unsupported multi-host workers.
- **Downloads are blocked:** the resolved address is private/reserved or a redirect entered a blocked network.
- **Daily memory spike:** ensure `ConcurrentDatabaseReload no` is active and allocate 8 GiB RAM.
- **Valkey events absent:** check `/v1/stats`; scans continue while the publisher retries its outbox.
- **A key was lost:** create a new key; plaintext keys cannot be recovered.

## License

AVaaS is licensed under the [GNU General Public License v3.0](LICENSE). ClamAV, dependencies, and container images retain their respective licenses.
