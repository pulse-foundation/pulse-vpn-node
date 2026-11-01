FROM golang:1.27-bookworm@sha256:d9b6a1beb9474adc2a1ce842573639b44b2a0dd49b6669f51d3821baf9ec37c9 AS build
WORKDIR /src
ENV GOTOOLCHAIN=local GOPROXY=off
COPY go.mod go.sum ./
COPY internal/node ./internal/node
COPY tests/integration/realitylab ./tests/integration/realitylab
RUN CGO_ENABLED=0 go build -trimpath -o /out/reality-lab ./tests/integration/realitylab
FROM alpine:3.24@sha256:be2656061b536c1883c4034790e21d0a09c5aaaf51968b89368a3c0371ea2c9a
COPY --from=build /out/reality-lab /usr/local/bin/reality-lab
USER 10001:10001
ENTRYPOINT ["/usr/local/bin/reality-lab"]
