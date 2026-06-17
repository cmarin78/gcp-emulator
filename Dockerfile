# --- Etapa de build ---
FROM golang:1.22-alpine AS builder

WORKDIR /src

# Cacheable: solo se reinstalan dependencias si go.mod/go.sum cambian.
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" \
    -o /out/gcp-emulator ./cmd/server

# --- Etapa final: imagen mínima, sin toolchain de Go ---
FROM alpine:3.20

RUN apk add --no-cache ca-certificates && \
    addgroup -S emulator && adduser -S emulator -G emulator

WORKDIR /app
COPY --from=builder /out/gcp-emulator ./gcp-emulator
COPY web/console ./web/console

RUN mkdir -p /data && chown -R emulator:emulator /app /data
USER emulator

ENV EMULATOR_ADDR=:8443
ENV EMULATOR_DB=/data/emulator.db
ENV EMULATOR_WEB=/app/web/console

EXPOSE 8443
VOLUME ["/data"]

ENTRYPOINT ["./gcp-emulator"]
