# syntax=docker/dockerfile:1
# Anora Runner service image.
FROM golang:1.27.2-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/anora ./cmd/anora

FROM debian:bookworm-slim
ARG TARGETARCH
ARG GITLEAKS_VERSION=8.30.1
RUN apt-get update \
 && apt-get install -y --no-install-recommends git ca-certificates curl \
 && arch="$([ "$TARGETARCH" = "arm64" ] && echo arm64 || echo x64)" \
 && curl -fsSL "https://github.com/gitleaks/gitleaks/releases/download/v${GITLEAKS_VERSION}/gitleaks_${GITLEAKS_VERSION}_linux_${arch}.tar.gz" \
    | tar -xz -C /usr/local/bin gitleaks \
 && apt-get purge -y curl && apt-get autoremove -y && rm -rf /var/lib/apt/lists/* \
 && useradd -u 1000 -m -s /usr/sbin/nologin anora \
 && mkdir -p /srv/anora/workspaces && chown 1000:1000 /srv/anora/workspaces
COPY --from=build /out/anora /usr/local/bin/anora
USER 1000:1000
ENV HTTP_ADDR=:8080 WORKSPACES_DIR=/srv/anora/workspaces
EXPOSE 8080
ENTRYPOINT ["anora"]
CMD ["serve"]
