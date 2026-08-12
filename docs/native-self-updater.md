# Native self-updater proposal

## Decision and boundaries

AVaaS should provide a manual, transactional updater for native Linux installations. It should reuse the GitHub Releases client and stable/beta channel policy used by `/health/ready`, but installation must only begin after an operator runs an explicit root command.

The first version will expose:

```text
avaas update check [--json]
sudo avaas update apply --yes [--version VERSION]
```

`check` is read-only and may run as the `avaas` user. `apply` requires UID 0 and `--yes`; it must not prompt, run from an HTTP endpoint, run from a timer, or update a Docker deployment. Containers continue to report availability and are upgraded by rebuilding or pulling and recreating the stack.

Only Linux `amd64` and `arm64` release archives are eligible. The repository is fixed to `BenAChilds/AVaaS`; neither commands nor configuration may redirect privileged downloads to another repository or URL. The default policy refuses downgrades and cross-channel targets. A beta installation may select a newer beta or stable release, while a stable installation may select only a newer stable release. `--version` selects an eligible published version; it does not bypass these rules.

## Command contracts

### `avaas update check`

The command performs a fresh bounded release check rather than relying on the API process's in-memory cache. Human output shows current version, channel, result, latest eligible version, release URL, and check time. JSON output uses the same update-status object returned by `/health/ready`. Network, parsing, and rate-limit failures return a non-zero exit status and a sanitized error; “already current” returns zero.

Development or malformed build versions are not updatable and return a clear non-zero result. Disabled background checks do not disable an explicit CLI check.

### `avaas update apply`

The command refuses to start unless all of these checks pass:

- the process is root on Linux and the architecture is supported;
- `/usr/local/bin/avaas` is the running installation path and is a regular, root-owned file rather than a symlink;
- the release is eligible, newer, published, and not a draft;
- `avaas.target` and the expected native directories exist;
- `/run/avaas/update.lock` can be acquired exclusively with `flock`;
- the staging and backup locations are local filesystems with sufficient free space;
- no incomplete update requires recovery.

Safe paths and timeouts are compiled defaults, not required configuration:

| Purpose | Default |
| --- | --- |
| Lock | `/run/avaas/update.lock` |
| Transaction state | `/var/lib/avaas/update-state.json` |
| Staging parent | `/var/lib/avaas/updates` |
| Backup parent | `/var/lib/avaas/update-backups` |
| HTTP timeout | 30 seconds per request |
| Total download timeout | 15 minutes |
| Post-start verification | 3 minutes |

Each transaction uses newly created mode-`0700` directories named from a generated identifier, not from untrusted release text. Initial implementation should not add path overrides. If later required, overrides must be absolute, clean, local paths beneath `/var/lib/avaas` or `/run/avaas` and must reject symlinks.

## Verification and staging

1. Fetch the selected release through the pinned GitHub API using HTTPS-only redirects and bounded response sizes.
2. Select exactly `avaas_<tag>_linux_<goarch>.tar.gz`, `checksums.txt`, and the release's provenance for that asset. Reject missing, duplicate, renamed, or unexpected architecture assets.
3. Download into the private staging directory using `O_CREATE|O_EXCL`, maximum-size limits, `fsync`, and an atomic final rename. Partial files never become candidates for installation.
4. Verify the archive bytes against the SHA-256 digest in GitHub's release-asset metadata and the exact filename entry in `checksums.txt`. Both values must agree.
5. Verify the GitHub Actions build-provenance attestation for the same digest, repository, workflow identity, and release tag. Implementation may use GitHub's attestation API/library; it must not shell out to an unpinned executable or accept an unsigned fallback.
6. Extract into a second private directory. Reject absolute paths, `..`, links, devices, sockets, duplicate entries, oversized entries, excessive entry counts, and files outside the release allowlist.
7. Require the expected binary, README, LICENSE, CHANGELOG, `configs/`, `systemd/`, and `scripts/` payload. Run the staged binary's `version --json` with a minimal environment and confirm its version exactly matches the selected tag.

Verification happens while AVaaS remains online. Any failure deletes only the transaction's staging directory, leaves the installation untouched, records a sanitized failure, and releases the lock.

## Transaction and rollback

After verification, write and `fsync` the transaction state before every phase transition. State contains the transaction ID, from/to versions, phase, staging and backup paths, verified archive digest, timestamps, and the last sanitized error. It must contain no credentials, API keys, URLs with query strings, or secret contents.

Phases are:

1. `verified`: artifact and provenance checks passed.
2. `stopped`: `avaas.target` is inactive.
3. `backed_up`: the complete recovery set has been copied and synced.
4. `installed`: new binary and unit/config candidates are in place.
5. `migrated`: the staged release successfully ran database migrations.
6. `started`: `avaas.target` is active.
7. `verified_running`: `doctor` and `/health/ready` succeeded.
8. `complete`: the transaction is durable and staging may be removed.

The backup is created beneath `/var/lib/avaas/update-backups/<transaction>` with mode `0700`. It contains:

- `/usr/local/bin/avaas`;
- `/var/lib/avaas/avaas.db`, `avaas.db-wal`, and `avaas.db-shm` when present;
- all of `/etc/avaas`, preserving ownership and modes so secrets remain paired with the database;
- installed `avaas-*.service`, `avaas-*.path`, and `avaas.target` units.

Stop `avaas.target` before copying SQLite. Copy regular files without following symlinks, preserve metadata, `fsync` files and directories, and write a SHA-256 manifest for the backup. A missing optional WAL/SHM file is recorded rather than synthesized.

Install the binary through a same-directory temporary file followed by `fsync`, mode/owner validation, and atomic rename. Install packaged systemd units atomically and run `systemctl daemon-reload`. Preserve every existing configuration file; changed packaged defaults are written as root-owned `.new` files exactly as the current installer does. Never replace secrets.

Run migrations as the `avaas` user with the installed configuration. Then start `avaas.target`, run `avaas doctor`, and poll the local `/health/ready` endpoint until it returns ready or the verification timeout expires. Only then mark the transaction complete. Retain at least the latest successful backup for operator rollback; define retention by count only after measuring backup size in real deployments.

Any failure from `stopped` through `verified_running` invokes rollback automatically:

1. stop `avaas.target`;
2. verify the backup manifest;
3. restore the binary, database plus sidecars, complete `/etc/avaas`, and units as one set;
4. run `systemctl daemon-reload`;
5. start `avaas.target` and run the previous binary's `doctor`;
6. record `rolled_back` or `rollback_failed` and preserve all evidence for the operator.

Rollback does not attempt to reverse a schema migration using only the old binary. It restores the matching pre-update database and secrets. A rollback failure is terminal: the command leaves AVaaS stopped and prints explicit recovery paths rather than repeatedly mutating the host.

## Interruption recovery

Every `apply` invocation first inspects `update-state.json` while holding the lock:

- `verified`: discard staging safely or resume before service shutdown.
- `stopped`: resume backup creation; if its completeness cannot be proven, restart the old target without installing.
- `backed_up`, `installed`, or `migrated`: automatically roll back from the verified complete backup.
- `started`: rerun post-start verification; mark complete only on success, otherwise roll back.
- `verified_running`: mark complete after syncing state.
- `complete` or `rolled_back`: archive the state and start a new transaction.
- `rollback_failed`: refuse further automatic changes and print manual recovery instructions.

State writes use a same-directory temporary file, `fsync`, and atomic rename. Signal handling records the current phase; signals are deferred during individual atomic file replacements. Power loss at any point therefore results in either an untouched old file, a complete new file, or a recoverable phase backed by the transaction record.

## Release-pipeline requirements

Before enabling `update apply`, stable and beta workflows must:

- keep deterministic asset names for Linux `amd64` and `arm64`;
- publish `checksums.txt` after all archives are finalized;
- attach GitHub Actions provenance for every archive and retain the existing repository/workflow identity;
- prevent a release asset from being replaced after publication, using immutable releases when available;
- test each archive's `version --json`, file allowlist, installer layout, checksum, and attestation in CI;
- run updater integration tests against a disposable systemd-capable VM for successful upgrades, migration failure, health failure, interruption recovery, and rollback.

The feature remains proposal-only until those pipeline guarantees and VM tests are implemented.

## Threat model and audit requirements

- A compromised mirror or network cannot install bytes that fail the GitHub digest, checksum, or provenance checks.
- A compromised GitHub release alone is insufficient unless it also has valid expected-workflow provenance; repository/workflow compromise remains a trusted-root risk and must be documented.
- Archive traversal, link attacks, device nodes, decompression bombs, and unexpected files are rejected before privileged installation.
- The updater never accepts arbitrary URLs, repositories, commands, paths, or release channels from HTTP clients.
- The exclusive lock prevents concurrent updates; transaction state makes crashes and power loss recoverable.
- All subprocesses use fixed executable paths, explicit arguments, minimal environments, bounded timeouts, and no shell interpolation.
- Logs record transaction ID, versions, phases, digests, and sanitized errors. They never record tokens, secret contents, API-key identifiers, or query-bearing download URLs.
- Existing API and worker processes remain unprivileged. Only the explicit root CLI command crosses the installation privilege boundary.

## Acceptance criteria for future implementation

- Existing native installations need no new environment variables.
- `update check` and health choose identical eligible releases for stable, beta, current, malformed, draft, and downgrade cases.
- `update apply` cannot mutate a Docker installation, follow a symlink, use an arbitrary repository, or proceed without `--yes` and root.
- Corrupt, oversized, unsigned, wrongly attested, or malicious archives leave the running installation unchanged.
- Failures after shutdown restore and start the exact previous binary, database, secrets, configuration, and units.
- Forced termination at every transaction phase produces the documented recovery behavior on the next invocation.
