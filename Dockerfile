# Container image of the OPC UA adapter (Go). The Python preview adapters
# have their own image (adapters/Dockerfile) and are not part of releases.
# Built for linux/amd64 and linux/arm64 by .github/workflows/release.yml;
# the build stage cross-compiles on the build platform.
FROM --platform=$BUILDPLATFORM golang:1.24-alpine AS build
ARG VERSION=0.1.0-dev
ARG TARGETOS=linux
ARG TARGETARCH
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /out/trailsource ./cmd/trailsource

FROM alpine:3.21
RUN adduser -D -H -u 10001 appuser && mkdir -p /state /journal /pki && chown appuser /state /journal /pki
COPY --from=build /out/trailsource /usr/local/bin/trailsource
USER 10001
ENTRYPOINT ["trailsource"]
CMD ["help"]
