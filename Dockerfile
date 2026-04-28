FROM node:24 AS webui-builder

ARG HTTP_PROXY
ARG HTTPS_PROXY
ARG NO_PROXY
ARG http_proxy
ARG https_proxy
ARG no_proxy

WORKDIR /app/webui
COPY webui/package.json webui/package-lock.json ./
RUN npm ci
COPY config.example.json /app/config.example.json
COPY webui ./
RUN npm run build

FROM golang:1.26 AS go-builder
WORKDIR /app
ARG TARGETOS
ARG TARGETARCH
ARG BUILD_VERSION
ARG GOPROXY=https://proxy.golang.org,direct
ARG GOSUMDB=sum.golang.org
ARG HTTP_PROXY
ARG HTTPS_PROXY
ARG NO_PROXY
ARG http_proxy
ARG https_proxy
ARG no_proxy
ENV GOPROXY=${GOPROXY}
ENV GOSUMDB=${GOSUMDB}
COPY go.mod go.sum* ./
RUN go mod download
COPY . .
RUN set -eux; \
    GOOS="${TARGETOS:-$(go env GOOS)}"; \
    GOARCH="${TARGETARCH:-$(go env GOARCH)}"; \
    BUILD_VERSION_RESOLVED="${BUILD_VERSION:-}"; \
    if [ -z "${BUILD_VERSION_RESOLVED}" ] && [ -f VERSION ]; then BUILD_VERSION_RESOLVED="$(cat VERSION | tr -d "[:space:]")"; fi; \
    CGO_ENABLED=0 GOOS="${GOOS}" GOARCH="${GOARCH}" go build -buildvcs=false -ldflags="-s -w -X ds2api/internal/version.BuildVersion=${BUILD_VERSION_RESOLVED}" -o /out/ds2api ./cmd/ds2api

FROM busybox:1.36.1-musl AS busybox-tools

FROM ghcr.io/cjackhwang/ds2api:latest AS upstream-runtime

FROM debian:bookworm-slim AS runtime-base
WORKDIR /app
COPY --from=upstream-runtime /etc/ssl/certs /etc/ssl/certs
COPY --from=busybox-tools /bin/busybox /usr/local/bin/busybox
EXPOSE 5001
CMD ["/usr/local/bin/ds2api"]

FROM runtime-base AS runtime-from-source
COPY --from=go-builder /out/ds2api /usr/local/bin/ds2api

COPY --from=go-builder /app/config.example.json /app/config.example.json
COPY --from=webui-builder /app/static/admin /app/static/admin

FROM busybox-tools AS dist-extract
ARG TARGETARCH
COPY dist/docker-input/linux_amd64.tar.gz /tmp/ds2api_linux_amd64.tar.gz
COPY dist/docker-input/linux_arm64.tar.gz /tmp/ds2api_linux_arm64.tar.gz
RUN set -eux; \
    case "${TARGETARCH}" in \
      amd64) ARCHIVE="/tmp/ds2api_linux_amd64.tar.gz" ;; \
      arm64) ARCHIVE="/tmp/ds2api_linux_arm64.tar.gz" ;; \
      *) echo "unsupported TARGETARCH: ${TARGETARCH}" >&2; exit 1 ;; \
    esac; \
    tar -xzf "${ARCHIVE}" -C /tmp; \
    PKG_DIR="$(find /tmp -maxdepth 1 -type d -name "ds2api_*_linux_${TARGETARCH}" | head -n1)"; \
    test -n "${PKG_DIR}"; \
    mkdir -p /out/static; \
    cp "${PKG_DIR}/ds2api" /out/ds2api; \
    cp "${PKG_DIR}/config.example.json" /out/config.example.json; \
    cp -R "${PKG_DIR}/static/admin" /out/static/admin

FROM runtime-base AS runtime-from-dist
COPY --from=dist-extract /out/ds2api /usr/local/bin/ds2api

COPY --from=dist-extract /out/config.example.json /app/config.example.json
COPY --from=dist-extract /out/static/admin /app/static/admin

FROM runtime-from-source AS final
