# Update these pinned references as part of reviewed dependency upgrades.
ARG WORKSPACE_IMAGE=debian:trixie-slim@sha256:a29215f6a35e51e22adffa17f89e9d2ef06214e64a2bad10d765c46aea49f11f
ARG GO_IMAGE=golang:1.27.3-alpine@sha256:f2c872bcee949b7be9c490efd02b48a8a2fbf947cdf533f1c58525ec9d8170fd
ARG RUNTIME_IMAGE=alpine:3.23@sha256:85fe1e81d6758c208f3e1eed4338a1997e19d4be002d4dd32d3100c9a8c010a0
ARG NODE_IMAGE=node:24.11.1-alpine3.23@sha256:682368d8253e0c3364b803956085c456a612d738bd635926d73fa24db3ce53d7

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
RUN apk add --no-cache ca-certificates git openssh-client && addgroup -g 1000 agent && adduser -D -u 1000 -G agent agent
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
RUN curl -fsSL https://codeberg.org/forgejo-contrib/forgejo-cli/releases/download/v0.6.0/forgejo-cli-x86_64-linux.tar.gz -o /tmp/fj.tar.gz \
    && echo 'ea559da5449b6dd7e0dd9f7ea51c906b575696f782edd606b52adcf773602742  /tmp/fj.tar.gz' | sha256sum -c - \
    && mkdir /opt/fj && tar xzf /tmp/fj.tar.gz -C /opt/fj \
    && rm /tmp/fj.tar.gz
COPY scripts/apc-fj.py /usr/local/bin/fj
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
COPY scripts/apc-provider.py /usr/local/bin/apc-provider
RUN ln -s apc-provider /usr/local/bin/grok \
    && ln -s apc-provider /usr/local/bin/cursor-agent \
    && ln -s apc-provider /usr/local/bin/muse
COPY --from=build /agent-project-controller /usr/local/bin/agent-project-controller
ENTRYPOINT ["/usr/local/bin/agent-project-controller"]
COPY third_party/licenses/ /usr/share/agent-project-controller/licenses/
USER 1000:1000
CMD ["controller"]

# Trusted T3 Connect registrar. Project T3 servers and agent CLIs stay in workers.
FROM ${WORKSPACE_IMAGE} AS t3-controller
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates curl \
    && rm -rf /var/lib/apt/lists/* \
    && groupadd -g 1000 agent && useradd -m -u 1000 -g 1000 agent
COPY --from=build /agent-project-controller /usr/local/bin/agent-project-controller
COPY --from=workspace /opt/t3 /opt/t3
COPY third_party/licenses/ /usr/share/agent-project-controller/licenses/
RUN curl -fsSL https://github.com/cloudflare/cloudflared/releases/download/2026.5.2/cloudflared-linux-amd64 -o /opt/t3/cloudflared \
    && echo '5286698547f03df745adb2355f04c12dde52ef425491e81f433642d695521886  /opt/t3/cloudflared' | sha256sum -c - \
    && chmod 0755 /opt/t3/cloudflared
USER 1000:1000
ENTRYPOINT ["/usr/local/bin/agent-project-controller"]
CMD ["controller"]
