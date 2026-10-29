FROM golang:1.26-bookworm@sha256:23ce71337b726608afb1bc00986e630a05034a87a87ea1fc2cd1ae5cf33387eb AS build
WORKDIR /src
ENV GOTOOLCHAIN=local GOPROXY=off
COPY go.mod ./
COPY internal/node ./internal/node
COPY tests/integration/realitylab ./tests/integration/realitylab
RUN CGO_ENABLED=0 go build -trimpath -o /out/reality-lab ./tests/integration/realitylab
FROM alpine:3.22@sha256:14358309a308569c32bdc37e2e0e9694be33a9d99e68afb0f5ff33cc1f695dce
COPY --from=build /out/reality-lab /usr/local/bin/reality-lab
USER 10001:10001
ENTRYPOINT ["/usr/local/bin/reality-lab"]
