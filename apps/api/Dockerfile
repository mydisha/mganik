# syntax=docker/dockerfile:1

# Satu image untuk dua peran: `api serve` (HTTP + worker) dan `api migrate`.

FROM golang:1.27-alpine AS build
WORKDIR /src

# Layer dependency hanya bergantung pada manifest modul.
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    GOMODCACHE=/go/pkg/mod go mod download

COPY . .
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    GOMODCACHE=/go/pkg/mod GOCACHE=/root/.cache/go-build CGO_ENABLED=0 GOOS=linux \
    go build -trimpath -ldflags="-s -w" -o /out/api ./cmd/api

FROM alpine:3.22
RUN apk add --no-cache ca-certificates tzdata \
    && addgroup -S -g 1001 app \
    && adduser -S -D -H -u 1001 -G app app
COPY --from=build /out/api /usr/local/bin/api

USER app
ENV HTTP_ADDR=:8080
EXPOSE 8080
HEALTHCHECK --interval=10s --timeout=3s --start-period=10s --retries=3 \
    CMD wget -q -O /dev/null http://127.0.0.1:8080/healthz || exit 1

ENTRYPOINT ["api"]
CMD ["serve"]
