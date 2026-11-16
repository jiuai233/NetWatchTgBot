# NetWatch 部署与发布指南

## 1. 环境前置条件

- 本地构建环境：Go 1.25+（纯静态编译 `CGO_ENABLED=0`，无 C 依赖）。
- 服务端运行环境：Linux `amd64` 或 `arm64`。
- 主控端中文字体：主控目录 `/opt/netwatch/font.ttf`（或通过环境变量 `NETWATCH_FONT` 指定 `.ttf` / `.ttc` 中文字体路径）。

---

## 2. 本机交叉编译与推送 GitHub Release

在本地执行以下命令，生成面向 Linux `amd64` 与 `arm64` 的全部二进制产物到 `dist/` 目录：

```bash
mkdir -p dist
for arch in amd64 arm64; do
  GOOS=linux GOARCH=$arch CGO_ENABLED=0 go build -ldflags="-s -w" -o "dist/netwatch-master-linux-${arch}" ./cmd/netwatch
  GOOS=linux GOARCH=$arch CGO_ENABLED=0 go build -ldflags="-s -w" -o "dist/netwatch-agent-linux-${arch}" ./cmd/netwatch-agent
done
cp install.sh dist/
```

使用 GitHub CLI 创建标签并上传 Release（或在 GitHub 网页端手动新建 Release 并上传 `dist/` 中的文件）：

```bash
git tag v0.1.0
git push origin v0.1.0
gh release create v0.1.0 dist/* --title "v0.1.0" --notes "Linux amd64/arm64 二进制包"
```

---

## 3. 新机器部署 Agent 并自动注册到主控

主控已启用基于 `agent_token` 鉴权的自动注册机制。新 VPS 只需拉取 Release 二进制并携带 `register` 元数据启动，主控会自动将其写入 SQLite `nodes` 表并在 Telegram 看板与按钮中生效，无需修改主控配置或重启主控。

在新 VPS 上执行（按实际参数替换）：

```bash
curl -fsSL "https://github.com/jiuai233/NetWatchTgBot/releases/latest/download/install.sh" -o /tmp/nw-install.sh
REPO="jiuai233/NetWatchTgBot" \
MASTER="http://<主控IP>:18080/api/v1/report" \
TOKEN="<主控config.yaml中的agent_token>" \
NODE_ID="vps-jp" \
NODE_NAME="日本东京" \
PORT="443" \
PROTO="VLESS" \
QUOTA_GB="500" \
RESET_DAY="1" \
EXPIRE="2026-12-31" \
bash /tmp/nw-install.sh
```

可选环境变量：
- `PROBE_PORT`：主端口为 UDP 协议（如 Hysteria2）时，指定同机一个 TCP 端口供 Globalping TCPing 检测；留空或 `0` 时 UDP 协议自动退回 ICMP 探测。
- `CLASH_API` / `CLASH_SECRET`：节点本机若启用 `sing-box` Clash API（如 `http://127.0.0.1:9090`），填入后可按客户端来源 IP 统计精确流量与实时速率。

---

## 4. 主控部署与换机迁移

### 首次部署主控

1. 从 Release 下载对应架构的 `netwatch-master-linux-<arch>` 至 `/opt/netwatch/netwatch-master` 并赋予执行权限。
2. 放置中文字体至 `/opt/netwatch/font.ttf`。
3. 参照 `config.example.yaml` 创建 `/opt/netwatch/config.yaml`，填入 Telegram `bot_token` 与 `agent_token`（主控端口默认 `:18080`）。
4. 注册 systemd 服务启动：
   ```bash
   /opt/netwatch/netwatch-master master -config /opt/netwatch/config.yaml
   ```

### 更换主控服务器

所有历史流量、日统计、客户端 IP 累计消耗与自动注册的节点信息全部存储在 `/opt/netwatch/netwatch.db` 单文件中。换机时：

1. 将旧主控上的 `/opt/netwatch/netwatch.db` 与 `/opt/netwatch/config.yaml` 打包拷贝至新主控的 `/opt/netwatch/`。
2. 在新主控启动 `netwatch-master`（新旧主机可先停止旧主控再启新主控，避免同一个 `bot_token` 轮询冲突）。
3. 将各节点 `/opt/netwatch/agent.yaml` 中的 `master_url` 指向新主控 IP 后重启 `netwatch-agent`。

---

## 5. 常用 Telegram 交互命令

- `/start`：开启底部快捷面板并推送总览看板
- `/status`：查看节点总览（可切换卡片图 / 纯文字、今天 / 本周 / 本月）
- `/clients`：按 VPS 分组查看当前连入 IP、归属地与累计流量消耗
- `/gd`：立即通过 Globalping 广州/深圳探针检测各节点延迟与连通性
- `/set <节点ID> <已用GB>`：校准指定节点当前计费周期的已用流量（例如 `/set vps-154 320.4`）
