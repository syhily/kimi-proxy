# kimi-proxy

通过加密 KCP 隧道，把本机 `kimi web`（Kimi Code 网页版）暴露到公网服务器上，用域名即可访问。原理与 NPS/frp 类似：Client 主动连出建立隧道，Server 将公网 HTTP 连接复用（smux）转发回 Client。

```
浏览器 ──HTTPS──> [公网 Server] ──KCP(加密)+smux──> [本机 Client] ──> kimi web (127.0.0.1:随机端口)
```

特性：

- Server 与 Client 之间走 KCP（UDP），AES-256-GCM 加密，密钥由共享 token 派生（SHA-256），错误 token 在加密层就无法通信。
- 单条 KCP 连接上用 smux 复用任意数量的并发 HTTP/WebSocket 流。
- Client 启动后自动拉起 `kimi web --port <空闲端口> --no-open` 子进程，崩溃自动重启；隧道断线自动重连。
- 通过 `-public-host` 把公网域名传给 `kimi web --allowed-host`，绕过 Host 头（DNS rebinding）检查。

## 构建

```sh
cd kimi-proxy
go build -o bin/server ./cmd/server    # 本机
GOOS=linux GOARCH=amd64 go build -o bin/server-linux ./cmd/server   # 交叉编译给公网服务器
go build -o bin/client ./cmd/client
```

## 部署 Server（公网服务器）

```sh
./server -tunnel-addr :7000 -http-addr :8080 -token '换成强随机字符串'
# token 也可用环境变量：KIMI_PROXY_TOKEN=... ./server
```

- `-tunnel-addr`：KCP 隧道监听地址，**UDP** 端口，需在防火墙/安全组放行。
- `-http-addr`：公网 HTTP 入口。建议前面套一层 Caddy/Nginx 终止 TLS 并绑定域名：

```caddyfile
kimi.example.com {
    reverse_proxy 127.0.0.1:8080
}
```

## 运行 Client（自己的电脑）

```sh
./client -server kimi.example.com:7000 \
         -token '与 Server 相同的字符串' \
         -public-host kimi.example.com
```

启动后会：

1. 自动拉起 `kimi web`（随机空闲端口，`--no-open`），等待就绪；
2. 建立加密隧道并保持心跳，断线指数退避重连；
3. 打印访问地址（若能读到 `~/.kimi-code/server.token`，会直接拼出带 token 的 URL）：

```
access the web UI at https://kimi.example.com/#token=<你的kimi web bearer token>
```

浏览器打开该地址即可使用网页版 Kimi Code。Web UI 自身的 bearer token 鉴权仍然生效，隧道只是传输通道。

### Client 参数

| 参数 | 说明 |
| --- | --- |
| `-server` | Server 隧道地址 `host:port`（必填） |
| `-token` | 共享密钥，或用 `KIMI_PROXY_TOKEN` 环境变量 |
| `-public-host` | 公网域名，传给 `kimi web --allowed-host` 并用于打印访问 URL |
| `-kimi-bin` | kimi CLI 路径，默认 `kimi` |
| `-kimi-port` | 指定 kimi web 端口，默认 0（自动选空闲端口） |
| `-attach` | 不启动子进程，直接代理到已在运行的 kimi web（如 `127.0.0.1:58627`） |

## 安全说明

- token 请使用强随机值（如 `openssl rand -base64 32`），它同时是加密密钥的种子。
- 不要把 Server 的 `-http-addr` 直接裸暴露在公网 HTTP 上，前面务必加 TLS。
- `kimi web` 仍绑定 `127.0.0.1` 且保留 bearer token 鉴权；不要给它加 `--dangerous-bypass-auth`。
