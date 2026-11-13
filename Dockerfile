# Update these pinned references as part of reviewed dependency upgrades.
ARG WORKSPACE_IMAGE=debian:trixie-slim@sha256:a72b4433de1cf7d6bae0c4cee74d0547f58601a643b3375f070c51600d63b6b2
ARG GO_IMAGE=golang:1.27.4-alpine3.23@sha256:146e3b9c7c1e2ec3d3f5348c8a686151a75e1976e50efac9fc022ebe446a9a82
ARG RUNTIME_IMAGE=alpine:3.23@sha256:ff7f0c727dd796aa36787bfcd9a64bf35a6ce72a73451b3359fde849b6703d63
ARG NODE_IMAGE=node:24.22.2-alpine3.23@sha256:9b69af715c7f9d038150b54ccccbaeffa9064bbc93e2904c29591ca097316af1

# Console UI. Rebuilt from the lockfile so the image never trusts the committed bundle.
FROM ${NODE_IMAGE} AS console
WORKDIR /src/web
COPY web/package.json web/package-lock.json web/.npmrc ./
RUN --mount=type=cache,target=/root/.npm npm ci --no-audit --no-fund
COPY web/ ./
RUN npm run build

FROM ${GO_IMAGE} AS build
ARG VERSION=dev
ARG SOURCE_REVISION=unknown
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY api/ api/
COPY cmd/ cmd/
COPY internal/ internal/
COPY --from=console /src/internal/web/dist/ internal/web/dist/
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X main.version=${VERSION} -X main.commit=${SOURCE_REVISION}" -o /agent-project-controller ./cmd/agent-project-controller

FROM ${RUNTIME_IMAGE} AS runtime
# git >= 2.52.1-r0: CVE-2026-105437, CVE-2026-111760, CVE-2026-114466 (HIGH). The
# minimum also keeps a cached layer or a stale mirror from shipping an older git.
RUN apk add --no-cache ca-certificates 'git>=2.52.1-r0' openssh-client \
    && apk upgrade --no-cache openssh-client openssh-client-common openssh-keygen \
    && addgroup -g 1000 agent && adduser -D -u 1000 -G agent agent
COPY --from=build /agent-project-controller /usr/local/bin/agent-project-controller
USER 1000:1000
ENTRYPOINT ["/usr/local/bin/agent-project-controller"]
CMD ["controller"]

# glovecell-voice (docs/voice.md): the Matrix voice service and approval gate.
# It needs no console bundle, so it builds without the Node stage. -tags goolm
# selects mautrix's pure-Go Olm, so the image stays CGO_ENABLED=0.
FROM ${GO_IMAGE} AS voice-build
ARG VERSION=dev
ARG SOURCE_REVISION=unknown
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY api/ api/
COPY cmd/ cmd/
COPY internal/ internal/
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build CGO_ENABLED=0 go build -tags goolm -trimpath -ldflags="-s -w -X main.version=${VERSION} -X main.commit=${SOURCE_REVISION}" -o /glovecell-voice ./cmd/glovecell-voice

FROM ${RUNTIME_IMAGE} AS voice
RUN apk add --no-cache ca-certificates && addgroup -g 1000 agent && adduser -D -u 1000 -G agent agent
COPY --from=voice-build /glovecell-voice /usr/local/bin/glovecell-voice
COPY third_party/licenses/ /usr/share/agent-project-controller/licenses/
USER 1000:1000
ENTRYPOINT ["/usr/local/bin/glovecell-voice"]
CMD ["serve"]

# Optional coding environment. The controller and proxy can use the smaller runtime target.
FROM ${GO_IMAGE} AS toolchain
FROM ${WORKSPACE_IMAGE} AS workspace
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates git openssh-client build-essential python3 nodejs npm curl jq ripgrep bash \
    && apt-get install -y --only-upgrade zlib1g \
    && rm -rf /var/lib/apt/lists/* \
    && groupadd -g 1000 agent && useradd -m -u 1000 -g 1000 agent
COPY --from=toolchain /usr/local/go /usr/local/go
ENV PATH="/usr/local/go/bin:${PATH}" CGO_ENABLED=0
# Official musl package, verified against the release asset's SHA-256.
RUN curl -fsSL https://github.com/openai/codex/releases/download/rust-v0.159.0/codex-app-server-package-x86_64-unknown-linux-musl.tar.gz -o /tmp/codex.tar.gz \
    && echo 'de4f0638334bfe0b99ae1872983c574c5795fe709ce72a642d780befb46178ca  /tmp/codex.tar.gz' | sha256sum -c - \
    && mkdir /opt/codex && tar xzf /tmp/codex.tar.gz -C /opt/codex \
    && rm /tmp/codex.tar.gz \
    && ln -s /opt/codex/bin/codex-app-server /usr/local/bin/codex-app-server
# The CLI is a separate release asset; retain the complete app-server helpers alongside it.
RUN curl -fsSL https://github.com/openai/codex/releases/download/rust-v0.159.0/codex-x86_64-unknown-linux-musl.tar.gz -o /tmp/codex-cli.tar.gz \
    && echo '6e587a08cb39599816c598b07e17b4bcbf9d41b5c1c0793a4b632b6741e9cbc0  /tmp/codex-cli.tar.gz' | sha256sum -c - \
    && tar xzf /tmp/codex-cli.tar.gz -C /tmp \
    && mv /tmp/codex-x86_64-unknown-linux-musl /opt/codex/bin/codex \
    && ln -s /opt/codex/bin/codex /usr/local/bin/codex \
    && ln -s /opt/codex/bin/codex-code-mode-host /usr/local/bin/codex-code-mode-host \
    && rm /tmp/codex-cli.tar.gz \
    && codex --version
RUN curl -fsSL https://github.com/pingdotgg/t3code/releases/download/v0.0.45/t3-0.0.45-linux-x64.tar.gz -o /tmp/t3.tar.gz \
    && echo '10505ae74bc6a43cfab0fde0bf06a0e0d6862f74030591be994a640d48a0d6bd  /tmp/t3.tar.gz' | sha256sum -c - \
    && mkdir /opt/t3 && tar xzf /tmp/t3.tar.gz -C /opt/t3 --strip-components=1 \
    && rm /tmp/t3.tar.gz
COPY scripts/apc-codex /usr/local/bin/apc-codex
# Stock Forgejo client. The wrapper supplies the enrolled Git identity per call.
RUN curl --http1.1 --retry 3 -fsSL https://codeberg.org/forgejo-contrib/forgejo-cli/releases/download/v0.6.0/forgejo-cli-x86_64-linux.tar.gz -o /tmp/fj.tar.gz \
    && echo 'ea559da5449b6dd7e0dd9f7ea51c906b575696f782edd606b52adcf773602742  /tmp/fj.tar.gz' | sha256sum -c - \
    && mkdir /opt/fj && tar xzf /tmp/fj.tar.gz -C /opt/fj \
    && rm /tmp/fj.tar.gz
COPY scripts/apc-fj.py /usr/local/bin/fj
# Official GitHub CLI for brokered GitHub projects. It runs with a placeholder GH_TOKEN
# that the project proxy replaces (docs/design/github-brokering.md).
RUN curl -fsSL https://github.com/cli/cli/releases/download/v2.103.3/gh_2.103.3_linux_amd64.tar.gz -o /tmp/gh.tar.gz \
    && echo '4c806978ffce1cec70853302a56d70ac6da4f19bae7adca4e1a8ab00633dd91d  /tmp/gh.tar.gz' | sha256sum -c - \
    && tar xzf /tmp/gh.tar.gz -C /tmp gh_2.103.3_linux_amd64/bin/gh \
    && install -m 0755 /tmp/gh_2.103.3_linux_amd64/bin/gh /usr/local/bin/gh \
    && rm -rf /tmp/gh.tar.gz /tmp/gh_2.103.3_linux_amd64
# Stock subscription clients, pinned independently of the application runtimes.
RUN curl -fsSL https://x.ai/cli/grok-1.0.46-linux-x86_64.zst -o /tmp/grok.zst \
    && echo '3d6fff352acd5658758f9b7b627313468e779c86fb259273df40eda5bac9d713  /tmp/grok.zst' | sha256sum -c - \
    && apt-get update && apt-get install -y --no-install-recommends zstd \
    && mkdir /opt/grok && unzstd -f -o /opt/grok/grok /tmp/grok.zst \
    && chmod 0755 /opt/grok/grok && rm /tmp/grok.zst \
    && rm -rf /var/lib/apt/lists/*
RUN curl -fsSL https://downloads.cursor.com/lab/2026.10.19-6bbf2c4/linux/x64/agent-cli-package.tar.gz -o /tmp/cursor.tar.gz \
    && echo 'cf80cc18d8b33bf33e762353977ca1868cfc45290c92c0ffdcef83bb8b37b44f  /tmp/cursor.tar.gz' | sha256sum -c - \
    && mkdir /opt/cursor-agent && tar xzf /tmp/cursor.tar.gz -C /opt/cursor-agent --strip-components=1 \
    && rm /tmp/cursor.tar.gz
RUN curl -fsSL 'https://lookaside.facebook.com/lookaside/muse/download/?channel=muse&version=1.4.7-R6101.1&file=muse-x86-linux' -o /tmp/muse \
    && echo '36e3dac91f56c15572b95be55d70ee9fd9685cd9ab49f747bab4746c8d7c2518  /tmp/muse' | sha256sum -c - \
    && mkdir /opt/muse && mv /tmp/muse /opt/muse/muse && chmod 0755 /opt/muse/muse
# herdr-core (hc): headless runtime for Android/PC Herdr via a hub (docs/herdr.md).
# Inert until HERDR_CORE_ENABLED=true and OrchestratorClass herdr.
RUN curl -fsSL https://releases.herdr.dev/core/0.1.19/herdr-core-linux-x86_64.tar.gz -o /tmp/herdr-core.tar.gz \
    && echo '4adcfb423d52f24c5cc4a1e2cd2fcad1f62d28bb77876ac472c29082a3eaa424  /tmp/herdr-core.tar.gz' | sha256sum -c - \
    && mkdir /opt/herdr-core \
    && tar xzf /tmp/herdr-core.tar.gz -C /opt/herdr-core \
    && ln -s /opt/herdr-core/herdr-core /usr/local/bin/herdr-core \
    && ln -s /opt/herdr-core/herdr-core /usr/local/bin/hc \
    && rm /tmp/herdr-core.tar.gz \
    && hc --version
COPY scripts/apc-provider.py /usr/local/bin/apc-provider
RUN ln -s apc-provider /usr/local/bin/grok \
    && ln -s apc-provider /usr/local/bin/cursor-agent \
    && ln -s apc-provider /usr/local/bin/muse
COPY --from=build /agent-project-controller /usr/local/bin/agent-project-controller
ENTRYPOINT ["/usr/local/bin/agent-project-controller"]
COPY third_party/licenses/ /usr/share/agent-project-controller/licenses/
COPY --chmod=0755 scripts/check-go-toolchain.sh /usr/local/bin/apc-check-go-toolchain
USER 1000:1000
RUN /usr/local/bin/apc-check-go-toolchain
CMD ["controller"]

# cloudflared for T3 Connect, built from the pinned upstream tag/commit. The
# 2026.10.7 release binary still links golang.org/x/net v0.61.0
# (CVE-2026-78661, fixed in v0.62.0), so the image gate would fail on it.
# Drop the `go get` once an upstream release ships x/net >= v0.62.0 and go
# back to the release binary with its published SHA-256.
FROM ${GO_IMAGE} AS cloudflared-build
ARG CLOUDFLARED_VERSION=2026.10.7
ARG UPSTREAM_REV=872b241f94871665c1c157adefcf90c11f0ead96
RUN apk add --no-cache git \
    && git clone --depth 1 --branch "${CLOUDFLARED_VERSION}" https://github.com/cloudflare/cloudflared /src \
    && test "$(git -C /src rev-parse HEAD)" = "${UPSTREAM_REV}"
WORKDIR /src
RUN go get golang.org/x/net@v0.62.0 \
    && CGO_ENABLED=0 GOFIPS140=off go build -trimpath \
       -ldflags "-X main.Version=${CLOUDFLARED_VERSION}" -o /cloudflared ./cmd/cloudflared

# Trusted T3 Connect registrar. Project T3 servers and agent CLIs stay in workers.
FROM ${WORKSPACE_IMAGE} AS t3-controller
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates curl \
    && apt-get install -y --only-upgrade zlib1g \
    && rm -rf /var/lib/apt/lists/* \
    && groupadd -g 1000 agent && useradd -m -u 1000 -g 1000 agent
COPY --from=build /agent-project-controller /usr/local/bin/agent-project-controller
COPY --from=workspace /opt/t3 /opt/t3
COPY third_party/licenses/ /usr/share/agent-project-controller/licenses/
COPY --from=cloudflared-build --chmod=0755 /cloudflared /opt/t3/cloudflared
USER 1000:1000
ENTRYPOINT ["/usr/local/bin/agent-project-controller"]
CMD ["controller"]
