# kimi-proxy

通过加密 KCP 隧道，把本机 `kimi web`（Kimi Code 网页版）暴露到公网服务器上，用域名即可访问。原理与 NPS/frp 类似：Client 主动连出建立隧道，Server 将公网 HTTP 连接复用（smux）转发回 Client。

```
浏览器 ──HTTPS──> [公网 Server] ──KCP(加密)+smux──> [本机 Client] ──> kimi web (127.0.0.1:随机端口)
```

特性：

- Server 与 Client 之间的隧道支持两种传输：KCP（UDP，AES-256-GCM 加密）或 TCP（TLS 1.3，证书密钥同样由共享 token 派生，client 固定校验服务端身份）。密钥/身份均由共享 token 经 SHA-256 派生，错误 token 在加密层就无法通信。TCP 模式用于只能映射 TCP 端口的服务器。
- 单条隧道连接上用 smux 复用任意数量的并发 HTTP/WebSocket 流。
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

- `-tunnel-addr`：隧道监听地址。默认 KCP（**UDP**），需在防火墙/安全组放行 UDP；如果服务器只能映射 TCP 端口，加 `-tunnel-proto tcp`（TLS 加密），映射 TCP 即可：

```sh
./server -tunnel-proto tcp -tunnel-addr :7000 -http-addr :8080 -token '...'
# Docker: -p 31134:7000/tcp，并把 -tunnel-proto tcp 加在启动参数里
```
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

### Homebrew 安装（macOS）

本仓库同时是一个 Homebrew tap，可直接安装并托管为后台服务：

```sh
brew tap syhily/kimi-proxy https://github.com/syhily/kimi-proxy.git
brew install kimi-proxy

# 编辑配置（server / token / public_host 等）
$EDITOR "$(brew --prefix)/etc/kimi-proxy/config.json"

# 用 launchd 托管，开机自启、崩溃自动拉起
brew services start kimi-proxy
```

日志在 `$(brew --prefix)/var/log/kimi-proxy.log`。注意 `brew services` 下 PATH 很精简，如果 `kimi` CLI 不在标准路径，请在配置文件里把 `kimi_bin` 写成绝对路径。

### 配置文件

`-config` 指定一个 JSON 配置文件（见 `config.example.json`），命令行 flag 优先于配置文件：

```json
{
  "server": "kimi.example.com:7000",
  "token": "与 Server 相同的字符串",
  "public_host": "kimi.example.com",
  "kimi_bin": "kimi",
  "kimi_port": 0,
  "tunnel_proto": "kcp"
}
```

字段与下方 flag 一一对应（`kimi_bin` → `-kimi-bin`，`kimi_port` → `-kimi-port`，`kimi_token` → `-kimi-token`，`public_host` → `-public-host`，`tunnel_proto` → `-tunnel-proto`），也支持 `"attach"`。

### Client 参数

| 参数 | 说明 |
| --- | --- |
| `-config` | JSON 配置文件路径，flag 优先于文件中的值 |
| `-server` | Server 隧道地址 `host:port`（必填） |
| `-token` | 共享密钥，或用 `KIMI_PROXY_TOKEN` 环境变量 |
| `-public-host` | 公网域名，传给 `kimi web --allowed-host` 并用于打印访问 URL |
| `-kimi-bin` | kimi CLI 路径，默认 `kimi` |
| `-kimi-port` | 指定 kimi web 端口，默认 0（自动选空闲端口） |
| `-kimi-token` | 固定 kimi web 的 bearer token（以 `KIMI_CODE_PASSWORD` 传给子进程），默认空（kimi web 用持久化随机 token） |
| `-attach` | 不启动子进程，直接代理到已在运行的 kimi web（如 `127.0.0.1:58627`） |
| `-tunnel-proto` | 隧道传输：`kcp`（UDP，默认）或 `tcp`（TLS，用于只能映射 TCP 的服务器），需与 Server 一致 |

## 安全说明

- token 请使用强随机值（如 `openssl rand -base64 32`），它同时是加密密钥的种子。
- 不要把 Server 的 `-http-addr` 直接裸暴露在公网 HTTP 上，前面务必加 TLS。
- `kimi web` 仍绑定 `127.0.0.1` 且保留 bearer token 鉴权；不要给它加 `--dangerous-bypass-auth`。
