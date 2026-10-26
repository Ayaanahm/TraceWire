# syntax=docker/dockerfile:1.7
# One Dockerfile for every service: docker build --build-arg SERVICE=orders .
FROM golang:1.27.3-alpine AS build
ARG SERVICE
ARG VERSION=dev
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY . .
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    test -n "$SERVICE" && CGO_ENABLED=0 go build -trimpath \
    -ldflags="-s -w -X github.com/Ayaanahm/tracewire/internal/telemetry.version=${VERSION}" \
    -o /out/app ./cmd/${SERVICE}

# distroless static: no shell, no package manager, runs as non-root (65532)
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/app /app
USER nonroot:nonroot
ENTRYPOINT ["/app"]
