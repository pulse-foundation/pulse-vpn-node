FROM metacubex/mihomo:v1.19.33@sha256:9211361a3ed85305383288d38c05e85862d6d5a590c10007c37fc059cca82e9c AS core
FROM golang:1.26-bookworm@sha256:23ce71337b726608afb1bc00986e630a05034a87a87ea1fc2cd1ae5cf33387eb AS check
WORKDIR /src
ENV GOTOOLCHAIN=local
COPY go.mod go.sum ./
RUN go mod download
ENV GOPROXY=off
COPY cmd ./cmd
COPY internal ./internal
COPY tests ./tests
RUN test -z "$(gofmt -l cmd internal tests)" && go vet ./... && go test ./... && go test -race ./...
RUN go vet -tags=integration ./tests/integration && go test -tags=integration -run '^$' ./tests/integration
RUN CGO_ENABLED=0 go build -trimpath -o /out/pulse-vpn-node ./cmd/pulse-vpn-node

FROM alpine:3.22@sha256:14358309a308569c32bdc37e2e0e9694be33a9d99e68afb0f5ff33cc1f695dce AS runtime
COPY --from=check /out/pulse-vpn-node /usr/local/bin/pulse-vpn-node
COPY --from=core /mihomo /usr/local/bin/mihomo
COPY --from=check /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY LICENSE docs/dependencies.md /usr/share/doc/pulse-vpn-node/
RUN mkdir -p /run/pulse-node && chown 10001:10001 /run/pulse-node && chmod 700 /run/pulse-node
USER 10001:10001
ENV PULSE_NODE_LISTEN=0.0.0.0:8080
STOPSIGNAL SIGTERM
ENTRYPOINT ["/usr/local/bin/pulse-vpn-node"]
