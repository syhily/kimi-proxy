# kimi-proxy

通过加密隧道，把本机 `kimi web`（Kimi Code 网页版）暴露到公网服务器上，用域名即可访问。原理与 NPS/frp 类似：Client 主动连出建立隧道，Server 将公网 HTTP 连接复用（smux）转发回 Client。

```
浏览器 ──HTTPS──> [公网 Server] ──加密隧道(KCP/UDP 或 TCP/TLS)+smux──> [本机 Client] ──> kimi web (127.0.0.1)
```

特性：

- 隧道支持两种传输：**KCP**（UDP，AES-256-GCM 加密）或 **TCP**（TLS 1.3）。密钥/证书身份均由共享 token 经 HKDF-SHA256 按用途独立派生，client 与 server **双向固定校验**对方身份，错误 token 在加密层就无法通信。TCP 模式用于只能映射 TCP 端口的服务器。
- 单条隧道连接上用 smux 复用任意数量的并发 HTTP/WebSocket 流。
- Client 启动后自动拉起 `kimi web` 子进程（可固定端口和 bearer token），崩溃自动重启；隧道断线指数退避重连。
- 隧道双向保活：smux 层每 10s 互发 keepalive 帧（90s 无响应才判死，容忍网络抖动）；控制流另有应用层心跳（client 每 10s ping，server 回 pong，client 连续 60s 收不到 pong 即主动重连，比被动等待更快恢复）。
- TCP 模式隧道连接两端自动调优：`TCP_NODELAY` 低延迟 + keepalive（60s 空闲、15s 间隔、3 次探测，约 105s 识别半开连接/网络黑洞，先于 smux 超时释放悬挂连接）。
- 通过 `-public-host` 把公网域名传给 `kimi web --allowed-host`，绕过 Host 头（DNS rebinding）检查。

## 构建

```sh
cd kimi-proxy
go build -o bin/server ./cmd/server    # 本机
GOOS=linux GOARCH=amd64 go build -o bin/server-linux ./cmd/server   # 交叉编译给公网服务器
go build -o bin/client ./cmd/client
```

## 部署 Server（公网服务器）

直接运行：

```sh
./server -tunnel-addr :7000 -http-addr :8080 -token '换成强随机字符串'
# token 也可用环境变量：KIMI_PROXY_TOKEN=... ./server
```

或用 Docker（镜像默认 TCP 模式）：

```sh
docker build -t kimi-proxy-server .
docker run -d --name kimi-proxy \
  -e KIMI_PROXY_TOKEN='换成强随机字符串' \
  -p 7000:7000/tcp -p 8080:8080 \
  kimi-proxy-server
# KCP 模式则映射 UDP 并覆盖启动参数：
#   -p 7000:7000/udp ... kimi-proxy-server -tunnel-proto kcp
```

### Server 参数

| 参数 | 说明 |
| --- | --- |
| `-tunnel-addr` | 隧道监听地址，默认 `:7000`。KCP 模式为 **UDP** 端口，TCP 模式为 TCP 端口，需在防火墙/安全组按对应协议放行 |
| `-http-addr` | 公网 HTTP 入口，默认 `:8080`（TCP） |
| `-token` | 共享密钥（必填），或用 `KIMI_PROXY_TOKEN` 环境变量 |
| `-tunnel-proto` | 隧道传输：`kcp`（UDP，默认）或 `tcp`（TLS 1.3，用于只能映射 TCP 端口的服务器） |
| `-http-max-conns` | 公网 HTTP 最大并发连接数，默认 `256`，超限直接返回 503（防连接洪泛） |
| `-http-idle-timeout` | 转发连接空闲超时，默认 `10m`（`0` 关闭）。WebSocket 升级连接自动豁免（对话思考、挂机时不断连），只清理"连接了却不发数据的慢速占用" |
| `-tunnel-max-conns` | 隧道最大并发连接数（含未认证），默认 `8`。真实 client 只有 1 个，留足冗余防止被占满后无法重连 |
| `-tls-cert` / `-tls-key` | 可选，成对提供后 HTTP 入口直接启用 HTTPS（TLS 1.2+），无需前置 Caddy/Nginx |

`-http-addr` 可以前面套一层 Caddy/Nginx 终止 TLS 并绑定域名（未提供 `-tls-cert/-tls-key` 时建议）：

```caddyfile
kimi.example.com {
    reverse_proxy 127.0.0.1:8080
}
```

## 运行 Client（自己的电脑）

最小示例（KCP 模式）：

```sh
./client -server kimi.example.com:7000 \
         -token '与 Server 相同的字符串' \
         -public-host kimi.example.com
```

完整参数示例：

```sh
./client -server kimi.example.com:7000 \     # Server 隧道地址（必填）
         -token '与 Server 相同的字符串' \     # 共享密钥（必填，或 KIMI_PROXY_TOKEN）
         -public-host kimi.example.com \     # 公网域名，传给 kimi web --allowed-host
         -tunnel-proto kcp \                 # 与 Server 一致：kcp（默认）或 tcp
         -kimi-bin kimi \                    # kimi CLI 路径
         -kimi-port 0 \                      # kimi web 端口，0 = 自动选空闲端口
         -kimi-token '固定 bearer token'       # 可选，固定 kimi web 的访问 token
```

启动后会：

1. 自动拉起 `kimi web`（`--no-open`），等待就绪；
2. 建立加密隧道并保持心跳，断线指数退避重连；
3. 打印访问地址（带 bearer token 的完整 URL）：

```
access the web UI at https://kimi.example.com/#token=<你的kimi web bearer token>
```

浏览器打开该地址即可使用网页版 Kimi Code。Web UI 自身的 bearer token 鉴权仍然生效，隧道只是传输通道。

### Homebrew 安装（macOS）

本仓库同时是一个 Homebrew tap，可直接安装并托管为后台服务：

```sh
brew tap syhily/kimi-proxy https://github.com/syhily/kimi-proxy.git
brew install kimi-proxy

# 编辑配置（server / token / public_host / kimi_token 等）
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
  "kimi_token": "固定 bearer token（可选，留空则 kimi web 用持久化随机 token）",
  "tunnel_proto": "kcp",
  "attach": ""
}
```

字段与下方 flag 一一对应（`kimi_bin` → `-kimi-bin`，`kimi_port` → `-kimi-port`，`kimi_token` → `-kimi-token`，`public_host` → `-public-host`，`tunnel_proto` → `-tunnel-proto`，`attach` → `-attach`）。

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

- 隧道 token 请使用强随机值（如 `openssl rand -base64 32`）。它经 HKDF-SHA256 按用途独立派生为 KCP 加密密钥和 TLS 证书身份，两种用途的密钥互不通用。
- **v2 起 client 与 server 必须同步升级**：密钥派生方式已改变，且 TCP 模式改为双向证书固定，旧版与本版互相无法通信。
- TCP 隧道模式下，双方都固定校验对端证书公钥：不知道 token 的连接在 TLS 握手阶段就会被拒绝。
- 不要把 Server 的 `-http-addr` 直接裸暴露在公网 HTTP 上，前面务必加 TLS（前置 Caddy/Nginx，或直接使用 `-tls-cert/-tls-key`）。
- `kimi web` 的 bearer token 是公网访问的最终鉴权屏障，用 `-kimi-token` 固定时同样要使用强随机值。
- KCP 模式使用 UDP，无法在应用层拦截伪造源地址的洪水包（kcp-go 内部会为任意来源的包建立会话）。如服务器暴露公网 UDP 端口，建议用防火墙对 `-tunnel-addr` 端口限速，例如：

  ```sh
  # 仅示例：对 7000/udp 限速（iptables）
  iptables -A INPUT -p udp --dport 7000 -m limit --limit 100/s --limit-burst 200 -j ACCEPT
  iptables -A INPUT -p udp --dport 7000 -j DROP
  ```

- `kimi web` 仍绑定 `127.0.0.1`；不要给它加 `--dangerous-bypass-auth`。
