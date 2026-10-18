# syntax=docker/dockerfile:1
# LiteZap: imagem mínima com binário estático (modernc.org/sqlite dispensa CGO).
# Build:  docker build -t litezap .
# Rodar:  docker compose up -d   (veja docker-compose.yml)

FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY . .
ARG VERSION=dev
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" \
    -o /out/litezap ./cmd/litezap \
 && mkdir -p /out/data

# distroless/static: certificados CA, /tmp e usuário nonroot (65532), sem shell.
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/litezap /usr/local/bin/litezap
COPY --from=build --chown=65532:65532 /out/data /data
ENV LITEZAP_LISTEN_ADDR=0.0.0.0:8787 \
    LITEZAP_DATA_DIR=/data
VOLUME ["/data"]
EXPOSE 8787
USER nonroot:nonroot
# Sem HEALTHCHECK: a imagem não tem cliente HTTP e, fora do loopback, /healthz exige o token
# admin. Use o probe do orquestrador em /healthz (com o token) ou LITEZAP_OPS_LISTEN_ADDR.
ENTRYPOINT ["/usr/local/bin/litezap"]
# Arquivo de config opcional: se não existir, valem os padrões + variáveis LITEZAP_*.
CMD ["serve", "--config", "/etc/litezap/litezap.yaml"]
