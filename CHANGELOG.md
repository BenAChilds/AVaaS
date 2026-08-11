# Changelog

All notable changes are documented here following Keep a Changelog and semantic versioning.

## Unreleased

- Initial AVaaS implementation.
- Align the Docker clamd socket with the official image, add scan lifecycle logs, surface unreadable log sources, correct disabled-Valkey statistics, omit terminal retry timestamps, and add guarded statistics reset.
- Add an exact-host, development-only private download allowlist while preserving hard SSRF blocks for metadata and reserved destinations.
