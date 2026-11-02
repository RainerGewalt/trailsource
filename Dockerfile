FROM golang:1.24-alpine AS build
ARG VERSION=0.1.0-dev
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /out/trailsource ./cmd/trailsource

FROM alpine:3.21
RUN adduser -D -H -u 10001 appuser && mkdir -p /state /journal && chown appuser /state /journal
COPY --from=build /out/trailsource /usr/local/bin/trailsource
USER appuser
ENTRYPOINT ["trailsource"]
CMD ["run", "--config", "/config/opcua-extruder.yaml"]
