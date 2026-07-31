# Build stage
FROM golang:1.24-alpine AS build
WORKDIR /src

# Download dependencies first for better layer caching
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/server ./cmd/server

# Runtime stage
FROM alpine:3.21
RUN adduser -D -u 10001 app
COPY --from=build /out/server /usr/local/bin/server

USER app

# TLS-over-TCP tunnel (default; works on TCP-only platforms) and public HTTP
# entry. For KCP/UDP, expose 7000/udp and pass -tunnel-proto kcp instead.
EXPOSE 7000/tcp 8080/tcp

# Token is read from KIMI_PROXY_TOKEN; flags can override via CMD.
ENTRYPOINT ["server"]
CMD ["-tunnel-proto", "tcp", "-tunnel-addr", ":7000", "-http-addr", ":8080"]
