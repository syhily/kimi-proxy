# Build stage
FROM golang:1.24-alpine AS build
WORKDIR /src

# Dependencies are vendored (vendor/), so the build needs no network access.
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/server ./cmd/server

# Runtime stage
FROM alpine:3.21
RUN adduser -D -u 10001 app
COPY --from=build /out/server /usr/local/bin/server

USER app

# KCP tunnel (UDP) and public HTTP entry (TCP)
EXPOSE 7000/udp 8080/tcp

# Token is read from KIMI_PROXY_TOKEN; flags can override via CMD.
ENTRYPOINT ["server"]
CMD ["-tunnel-addr", ":7000", "-http-addr", ":8080"]
