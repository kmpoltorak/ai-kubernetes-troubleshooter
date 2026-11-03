# syntax=docker/dockerfile:1

FROM node:22-alpine AS web
WORKDIR /src/web
COPY web/package.json web/package-lock.json ./
RUN npm ci --no-audit --no-fund
COPY web ./
RUN npm run build

FROM golang:1.27-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
COPY migrations ./migrations
COPY --from=web /src/internal/api/ui/dist ./internal/api/ui/dist
ARG BUILD_VERSION=dev
ARG BUILD_REVISION=unknown
ARG BUILD_TIME=
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X 'github.com/kmpoltorak/ai-kubernetes-troubleshooter/internal/buildinfo.Version=${BUILD_VERSION}' -X 'github.com/kmpoltorak/ai-kubernetes-troubleshooter/internal/buildinfo.Revision=${BUILD_REVISION}' -X 'github.com/kmpoltorak/ai-kubernetes-troubleshooter/internal/buildinfo.BuiltAt=${BUILD_TIME}'" -o /out/api ./cmd/api

# Static binary on distroless: no shell, no package manager, non-root user.
FROM gcr.io/distroless/static-debian12:nonroot
ARG BUILD_VERSION=dev
ARG BUILD_REVISION=unknown
ARG BUILD_TIME=
LABEL org.opencontainers.image.version="${BUILD_VERSION}" \
      org.opencontainers.image.revision="${BUILD_REVISION}" \
      org.opencontainers.image.created="${BUILD_TIME}"
COPY --from=build /out/api /api
USER 65532:65532
EXPOSE 8080
# The Go binary is PID 1 and handles SIGTERM itself for graceful shutdown.
ENTRYPOINT ["/api"]
