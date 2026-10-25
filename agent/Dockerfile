# syntax=docker/dockerfile:1
FROM node:24-bookworm-slim AS build
WORKDIR /app
COPY package.json package-lock.json ./
RUN npm ci
COPY tsconfig.json ./
COPY src ./src
RUN npx tsc -p tsconfig.json && npm prune --omit=dev

FROM mcr.microsoft.com/playwright:v1.64.1-noble
ARG TARGETARCH
ARG GO_VERSION=1.27.2

RUN apt-get update \
 && apt-get install -y --no-install-recommends git python3 python3-pip python3-venv build-essential ripgrep jq ca-certificates curl \
 && rm -rf /var/lib/apt/lists/*

RUN curl -fsSL "https://go.dev/dl/go${GO_VERSION}.linux-${TARGETARCH}.tar.gz" | tar -C /usr/local -xz
ENV PATH=/usr/local/go/bin:/home/runner/go/bin:$PATH

# The runner user must be uid 1000 so workspaces are shared with Fleetpocket Runner.
RUN if getent passwd 1000 >/dev/null; then userdel -r "$(getent passwd 1000 | cut -d: -f1)"; fi \
 && useradd -m -u 1000 -s /bin/bash runner \
 && mkdir -p /home/runner/.claude /workspace \
 && chown -R runner:runner /home/runner /workspace \
 && git config --system --add safe.directory '*'

COPY --from=build /app/node_modules /app/node_modules
COPY --from=build /app/dist /app/dist
COPY package.json /app/package.json

USER runner
WORKDIR /workspace
ENV NODE_ENV=production
ENTRYPOINT ["node", "/app/dist/main.js"]
