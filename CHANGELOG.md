# Changelog

All notable changes are documented here following Keep a Changelog and semantic versioning.

## Unreleased

- Report cached, channel-aware AVaaS release availability on the readiness endpoint.
- Initial AVaaS implementation.
- Document the proposed manual, transactional native self-updater and its verification and rollback model.
- Document and add regression coverage for API and worker logs through the logs endpoint.
- Add an authenticated, rate-limited definition-update trigger for native and Compose deployments.
- Align the Docker clamd socket with the official image, add scan lifecycle logs, surface unreadable log sources, correct disabled-Valkey statistics, omit terminal retry timestamps, and add guarded statistics reset.
- Add an exact-host, development-only private download allowlist while preserving hard SSRF blocks for metadata and reserved destinations.
