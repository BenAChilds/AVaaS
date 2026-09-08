# syntax=docker/dockerfile:1.7
FROM golang:1.27.1-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY . .
ARG VERSION=dev
ARG COMMIT=unknown
ARG BUILD_DATE=unknown
RUN --mount=type=cache,target=/root/.cache/go-build CGO_ENABLED=0 go build -trimpath \
    -ldflags="-s -w -X main.version=${VERSION} -X main.commit=${COMMIT} -X main.buildDate=${BUILD_DATE}" \
    -o /out/avaas ./cmd/avaas

FROM alpine:3.22
RUN apk add --no-cache ca-certificates tzdata && addgroup -g 10001 avaas && adduser -D -H -u 10001 -G avaas avaas \
    && install -d -o avaas -g avaas -m 0750 /var/lib/avaas /var/log/avaas /var/spool/avaas /run/avaas /etc/avaas
COPY --from=build /out/avaas /usr/local/bin/avaas
USER 10001:10001
ENTRYPOINT ["/usr/local/bin/avaas"]
CMD ["serve"]
