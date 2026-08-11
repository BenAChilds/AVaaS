# Contributing to AVaaS

Use a supported Go release and Docker Compose. Open an issue before large behavioural changes. Keep changes focused, add tests, preserve API compatibility, and never commit credentials, presigned URLs, database files, or malware.

Before submitting a pull request, run:

```sh
gofmt -w .
go test -race ./...
go vet ./...
govulncheck ./...
docker compose config
```

Pull requests must explain the user-visible change and security implications, update the OpenAPI/README where appropriate, and pass CI. Commits should be clear and independently reviewable. By contributing, you agree that your contribution is licensed under GPL-3.0-only.

