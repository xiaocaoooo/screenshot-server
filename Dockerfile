# syntax=docker/dockerfile:1.7

FROM golang:1.23-alpine AS builder
WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o /out/screenshot-server .

FROM alpine:3.20
WORKDIR /app

RUN apk add --no-cache ca-certificates wget && adduser -D -u 10001 appuser

COPY --from=builder /out/screenshot-server /app/screenshot-server

ENV PORT=8080
EXPOSE 8080

USER appuser

HEALTHCHECK --interval=30s --timeout=5s --start-period=20s --retries=3 \
  CMD wget -qO- "http://127.0.0.1:${PORT}/health" >/dev/null || exit 1

ENTRYPOINT ["/app/screenshot-server"]
