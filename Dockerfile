# macro-collector: macrod (daemon) + macroctl (CLI) — ТЗ §21.1, D17.
# Tags checked on Docker Hub 2026-11-06 (LOOKUP U1).

FROM --platform=$BUILDPLATFORM golang:1.26.10-alpine3.24 AS build
ARG TARGETOS=linux
ARG TARGETARCH
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags="-s -w" -o /out/macrod ./cmd/macrod \
 && CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags="-s -w" -o /out/macroctl ./cmd/macroctl

# alpine, not distroless: phase 6 pushes exchange/ with the git CLI over ssh.
FROM alpine:3.24.4
RUN apk add --no-cache ca-certificates git openssh-client \
 && addgroup -S -g 10001 macro && adduser -S -D -H -u 10001 -G macro macro \
 && mkdir -p /app/data && chown macro:macro /app/data
WORKDIR /app
COPY --from=build /out/macrod /out/macroctl /usr/local/bin/
COPY prompts/ /app/prompts/
# GitHub host key (docs.github.com, githubs-ssh-key-fingerprints) for the exchange push
COPY deploy/github_known_hosts /app/deploy/github_known_hosts
USER macro
# config.yaml is mounted at /app/config.yaml; data_dir ./data → /app/data (volume).
VOLUME ["/app/data"]
ENTRYPOINT ["macrod"]
CMD ["--config", "/app/config.yaml"]
