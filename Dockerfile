FROM metacubex/mihomo:v1.19.33@sha256:9211361a3ed85305383288d38c05e85862d6d5a590c10007c37fc059cca82e9c AS core
FROM golang:1.27-bookworm@sha256:d9b6a1beb9474adc2a1ce842573639b44b2a0dd49b6669f51d3821baf9ec37c9 AS check
WORKDIR /src
ENV GOTOOLCHAIN=local
COPY go.mod go.sum ./
RUN test "$(go env GOVERSION)" = "go$(sed -n 's/^go //p' go.mod)"
RUN go mod download
ENV GOPROXY=off
COPY cmd ./cmd
COPY internal ./internal
COPY tests ./tests
RUN test -z "$(gofmt -l cmd internal tests)" && go vet ./... && go test ./... && go test -race ./...
RUN go vet -tags=integration ./tests/integration && go test -tags=integration -run '^$' ./tests/integration
ARG NODE_VERSION=dev
ARG NODE_COMMIT=unknown
RUN CGO_ENABLED=0 go build -ldflags="-X github.com/pulse-foundation/pulse-vpn-node/internal/buildinfo.version=${NODE_VERSION} -X github.com/pulse-foundation/pulse-vpn-node/internal/buildinfo.commit=${NODE_COMMIT}" -trimpath -o /out/pulse-vpn-node ./cmd/pulse-vpn-node

FROM alpine:3.24@sha256:be2656061b536c1883c4034790e21d0a09c5aaaf51968b89368a3c0371ea2c9a AS runtime
COPY --from=check /out/pulse-vpn-node /usr/local/bin/pulse-vpn-node
COPY --from=core /mihomo /usr/local/bin/mihomo
COPY --from=check /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY LICENSE docs/dependencies.md /usr/share/doc/pulse-vpn-node/
RUN mkdir -p /run/pulse-node && chown 10001:10001 /run/pulse-node && chmod 700 /run/pulse-node
USER 10001:10001
ENV PULSE_NODE_LISTEN=0.0.0.0:8080
STOPSIGNAL SIGTERM
ENTRYPOINT ["/usr/local/bin/pulse-vpn-node"]
