# syntax=docker/dockerfile:1
# One-shot repair overlay for the 2026-10-08 BuildKit cache corruption: several
# COPY layers shipped 0-byte binaries (cli-proxy-api, cellinit, docker, thv,
# communication-hub-telegram-auth) and 0-byte context files (hub-stt/hub-tts,
# seccomp JSONs). Rebuilds the artifact stages and overlays correct files onto
# the already-built dev images, then produces the missing cli-tools/cell-proxy
# tags. Delete once docker/Dockerfile rebuilds cleanly end-to-end.
FROM golang:1.27.1-bookworm AS gopatch
WORKDIR /src
COPY go.mod go.sum ./
COPY services/credential-broker ./services/credential-broker
RUN --mount=type=cache,id=hermes-go-mod,target=/go/pkg/mod go mod download
COPY cmd ./cmd
COPY internal ./internal
RUN --mount=type=cache,id=hermes-go-mod,target=/go/pkg/mod --mount=type=cache,id=hermes-go-build,target=/root/.cache/go-build CGO_ENABLED=0 go build -buildvcs=false -trimpath -ldflags='-s -w' -o /cellinit ./cmd/cellinit
RUN --mount=type=cache,id=hermes-go-mod,target=/go/pkg/mod --mount=type=cache,id=hermes-go-build,target=/root/.cache/go-build CGO_ENABLED=0 go build -tags telegramauth -buildvcs=false -trimpath -ldflags='-s -w' -o /communication-hub-telegram-auth ./cmd/communication

FROM golang:1.27.1-bookworm AS cliproxypatch
RUN --mount=type=cache,id=cliproxy-go-mod,target=/go/pkg/mod --mount=type=cache,id=cliproxy-go-build,target=/root/.cache/go-build set -e; git init /cliproxy && cd /cliproxy && git remote add origin https://github.com/router-for-me/CLIProxyAPI.git && for i in 1 2 3 4; do if git -c http.version=HTTP/1.1 fetch --depth 1 origin ba7e55836dee959e93ec6d41395865d9ec535086; then break; fi; if [ "$i" = 4 ]; then exit 1; fi; sleep 10; done && git checkout --detach FETCH_HEAD && CGO_ENABLED=0 go build -buildvcs=false -trimpath -ldflags='-s -w' -o /cli-proxy-api ./cmd/server

FROM docker:29-cli@sha256:018edbc908e08fcc9dbf029c812c34251e9b4719e6f71ca0e5eae2a987d014ca AS dockercli
FROM ghcr.io/stacklok/toolhive:v0.48.0@sha256:f1adc609e11e5f60fd5a27d23e7a63eb14dcfee13f3c0b2f1741680a64fcea5d AS toolhive

# Context COPYs into a lower-layer path are producing 0-byte files on this
# daemon; COPY --from= is unaffected. Stage the context files, then relay them.
FROM scratch AS ctxfiles
COPY docker/seccomp-buildkit-rootless.json /seccomp/seccomp-buildkit-rootless.json
COPY docker/seccomp-mcp-runtime.json /seccomp/seccomp-mcp-runtime.json
COPY docker/hub-stt /bin/hub-stt
COPY docker/hub-tts /bin/hub-tts

FROM hermes-hub:0.3.0-dev AS patch-dev
USER 0:0
COPY --from=cliproxypatch /cli-proxy-api /usr/local/bin/cli-proxy-api
COPY --from=ctxfiles /seccomp/ /opt/hub/seccomp/
COPY --from=ctxfiles /bin/ /usr/local/bin/
RUN chmod 0755 /usr/local/bin/hub-stt /usr/local/bin/hub-tts
USER 10001:10001

FROM hermes-hub:0.3.0-dev-control AS patch-control
USER 0:0
COPY --from=cliproxypatch /cli-proxy-api /usr/local/bin/cli-proxy-api
COPY --from=gopatch /cellinit /usr/local/bin/cellinit
COPY --from=dockercli /usr/local/bin/docker /usr/local/bin/docker
COPY --from=toolhive /ko-app/thv /usr/local/bin/thv
COPY --from=ctxfiles /seccomp/ /opt/hub/seccomp/
COPY --from=ctxfiles /bin/ /usr/local/bin/
RUN chmod 0755 /usr/local/bin/hub-stt /usr/local/bin/hub-tts /usr/local/bin/cellinit /usr/local/bin/thv /usr/local/bin/docker /usr/local/bin/cli-proxy-api
USER 10001:10001

FROM hermes-hub-telegram-auth:0.3.0-dev AS patch-auth
USER 0:0
COPY --from=cliproxypatch /cli-proxy-api /usr/local/bin/cli-proxy-api
COPY --from=gopatch /communication-hub-telegram-auth /usr/local/bin/communication-hub
COPY --from=ctxfiles /seccomp/ /opt/hub/seccomp/
COPY --from=ctxfiles /bin/ /usr/local/bin/
RUN chmod 0755 /usr/local/bin/hub-stt /usr/local/bin/hub-tts
USER 10001:10001

FROM debian:13.6-slim AS patch-cli-tools
RUN apt-get update && apt-get install -y --no-install-recommends git ripgrep ca-certificates && rm -rf /var/lib/apt/lists/*
COPY --from=gopatch /cellinit /cellinit
USER 10001:10001
ENTRYPOINT ["/cellinit"]

FROM debian:13.6-slim AS patch-cell-proxy
RUN apt-get update && apt-get install -y --no-install-recommends squid ca-certificates && rm -rf /var/lib/apt/lists/*
EXPOSE 3128
USER 10001:10001
CMD ["squid", "-N", "-f", "/etc/squid/squid.conf"]
