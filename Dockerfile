# syntax=docker/dockerfile:1

# Sphynx runs in one container: the Sphynx server and web UI, the Headscale
# control server, the official Tailscale client (for "Sphynx home") and
# Tailcat (for Sphynx Link). The upstream programs are taken from their
# pinned official release images and modules, not copied into this repo.

ARG GO_VERSION=1.27
ARG HEADSCALE_VERSION=0.29.6
ARG TAILSCALE_VERSION=v1.104.4

FROM headscale/headscale:${HEADSCALE_VERSION} AS headscale
FROM tailscale/tailscale:${TAILSCALE_VERSION} AS tailscale

FROM --platform=$BUILDPLATFORM golang:${GO_VERSION}-alpine AS build
ARG TARGETOS
ARG TARGETARCH
ARG TAILCAT_VERSION=v0.7.0
ARG VERSION=dev
ENV CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH}
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" -o /out/sphynx ./cmd/sphynx
# go install puts cross-compiled binaries in a GOOS_GOARCH subfolder.
RUN go install -trimpath -ldflags="-s -w" github.com/tailscale/tailcat/cmd/tailcat@${TAILCAT_VERSION} \
 && cp "$(find /go/bin -type f -name tailcat | head -n1)" /out/tailcat

# Alpine already ships the CA bundle; time zones come from Go's own
# zoneinfo.zip, which every bundled (Go) program reads via ZONEINFO.
FROM alpine:3.22
COPY --from=build /usr/local/go/lib/time/zoneinfo.zip /usr/local/share/zoneinfo.zip
COPY --from=headscale /ko-app/headscale /usr/local/bin/headscale
COPY --from=tailscale /usr/local/bin/tailscale /usr/local/bin/tailscaled /usr/local/bin/
COPY --from=build /out/sphynx /out/tailcat /usr/local/bin/
COPY LICENSE THIRD_PARTY_NOTICES.md /usr/share/doc/sphynx/

# HEADSCALE_CONFIG lets `docker exec sphynx headscale ...` find Sphynx's
# Headscale.
ENV SPHYNX_DATA=/data ZONEINFO=/usr/local/share/zoneinfo.zip \
    HEADSCALE_CONFIG=/data/headscale/config.yaml
VOLUME /data
# 3000 admin UI, 3001 Sphynx Chat for this PC, 8080 devices over plain
# HTTP, 80/443 devices over HTTPS, 3478/udp STUN for the built-in relay,
# 41642/udp Sphynx home WireGuard (41641 is left to the Tailscale app, so
# the PC can join the network as a device of its own too).
EXPOSE 3000/tcp 3001/tcp 8080/tcp 80/tcp 443/tcp 3478/udp 41642/udp
HEALTHCHECK --interval=30s --timeout=5s --start-period=40s \
  CMD wget -q -O /dev/null http://127.0.0.1:3000/api/session || exit 1
ENTRYPOINT ["/usr/local/bin/sphynx"]
