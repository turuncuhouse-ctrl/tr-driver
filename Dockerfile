# ---- Frontend ----
FROM node:20-alpine AS web
WORKDIR /web
COPY web/package.json web/package-lock.json ./
RUN npm ci --no-audit --no-fund
COPY web/ ./
RUN npm run build

# ---- Go build ----
FROM golang:1.24-alpine AS builder
WORKDIR /src
RUN apk add --no-cache git ca-certificates
COPY go.mod go.sum* ./
COPY . .
COPY --from=web /web/dist ./web/dist
ENV GOTOOLCHAIN=local
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w -X necipdrive/internal/version.Version=0.5.0" -o /out/necipdrive ./cmd/server

# ---- Runtime ----
FROM alpine:3.20
RUN apk add --no-cache ca-certificates tzdata wget \
  && adduser -D -u 1000 appuser \
  && mkdir -p /data/files /app/web/dist \
  && chown -R appuser:appuser /data /app

WORKDIR /app
COPY --from=builder /out/necipdrive /app/necipdrive
COPY --from=builder /src/web/dist /app/web/dist

ENV APP_ENV=production \
    HTTP_ADDR=:8080 \
    DATA_DIR=/data/files

USER appuser
EXPOSE 8080
VOLUME ["/data/files"]
HEALTHCHECK --interval=30s --timeout=5s --start-period=20s --retries=5 \
  CMD wget -qO- http://127.0.0.1:8080/healthz || exit 1

CMD ["/app/necipdrive"]
