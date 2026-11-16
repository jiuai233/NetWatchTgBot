#!/bin/sh
set -eu

# NetWatch Agent 一键安装与自动注册脚本
# 用法示例：
#   REPO="jiuai233/NetWatchTgBot" MASTER="http://MASTER_IP:18080/api/v1/report" \
#   TOKEN="your_agent_token" NODE_ID="vps-jp" NODE_NAME="日本东京" \
#   PORT="443" PROTO="VLESS" QUOTA_GB="500" RESET_DAY="1" EXPIRE="2026-12-31" \
#   sh install.sh

REPO="${REPO:-jiuai233/NetWatchTgBot}"
TAG="${TAG:-latest}"
MASTER="${MASTER:-}"
TOKEN="${TOKEN:-}"
NODE_ID="${NODE_ID:-$(hostname)}"
NODE_NAME="${NODE_NAME:-$NODE_ID}"
PORT="${PORT:-443}"
WATCH_PORTS="${WATCH_PORTS:-$PORT}"
PROBE_PORT="${PROBE_PORT:-0}"
PROTO="${PROTO:-VLESS}"
QUOTA_GB="${QUOTA_GB:-500}"
RESET_DAY="${RESET_DAY:-1}"
EXPIRE="${EXPIRE:-2026-12-31}"
CLASH_API="${CLASH_API:-}"
CLASH_SECRET="${CLASH_SECRET:-}"

if [ -z "$REPO" ] || [ -z "$MASTER" ]; then
  echo "缺少参数，需要提供 REPO 和 MASTER，例如："
  echo "  REPO=jiuai233/NetWatchTgBot MASTER=http://1.2.3.4:18080/api/v1/report TOKEN=xxx NODE_ID=vps-new PORT=443 sh install.sh"
  exit 1
fi

ARCH_RAW="$(uname -m)"
case "$ARCH_RAW" in
  x86_64|amd64) ARCH="amd64" ;;
  aarch64|arm64) ARCH="arm64" ;;
  *) echo "不支持的架构: $ARCH_RAW"; exit 1 ;;
esac

BIN_NAME="netwatch-agent-linux-${ARCH}"
if [ "$TAG" = "latest" ]; then
  URL="https://github.com/${REPO}/releases/latest/download/${BIN_NAME}"
else
  URL="https://github.com/${REPO}/releases/download/${TAG}/${BIN_NAME}"
fi

mkdir -p /opt/netwatch
echo "下载: $URL"
curl -fsSL -o /opt/netwatch/netwatch-agent "$URL"
chmod +x /opt/netwatch/netwatch-agent

cat > /opt/netwatch/agent.yaml << EOF
mode: agent
agent_token: "${TOKEN}"
master_url: "${MASTER}"

local_collector:
  enabled: true
  node_id: "${NODE_ID}"
  interface: ""
  watch_ports: [${WATCH_PORTS}]
  clash_api_url: "${CLASH_API}"
  clash_secret: "${CLASH_SECRET}"
  interval_sec: 30
  register:
    id: "${NODE_ID}"
    name: "${NODE_NAME}"
    host: ""
    port: ${PORT}
    probe_port: ${PROBE_PORT}
    protocol: "${PROTO}"
    quota_gb: ${QUOTA_GB}
    reset_day: ${RESET_DAY}
    expire_date: "${EXPIRE}"
EOF

if command -v systemctl >/dev/null 2>&1; then
  cat > /etc/systemd/system/netwatch-agent.service << 'EOF'
[Unit]
Description=NetWatch Agent
After=network.target

[Service]
Type=simple
WorkingDirectory=/opt/netwatch
ExecStart=/opt/netwatch/netwatch-agent -config /opt/netwatch/agent.yaml
Restart=always
RestartSec=3

[Install]
WantedBy=multi-user.target
EOF
  systemctl daemon-reload
  systemctl enable netwatch-agent
  systemctl restart netwatch-agent
  systemctl is-active netwatch-agent
elif command -v rc-service >/dev/null 2>&1; then
  cat > /etc/init.d/netwatch-agent << 'EOF'
#!/sbin/openrc-run
name="netwatch-agent"
command="/opt/netwatch/netwatch-agent"
command_args="-config /opt/netwatch/agent.yaml"
supervisor="supervise-daemon"
output_log="/var/log/netwatch-agent.log"
error_log="/var/log/netwatch-agent.err"

depend() {
    need net
}
EOF
  chmod +x /etc/init.d/netwatch-agent
  rc-update add netwatch-agent default >/dev/null 2>&1 || true
  rc-service netwatch-agent restart
  rc-service netwatch-agent status
else
  echo "未检测到 systemd 或 OpenRC，可手动启动："
  echo "  nohup /opt/netwatch/netwatch-agent -config /opt/netwatch/agent.yaml >/var/log/netwatch-agent.log 2>&1 &"
fi

echo "安装并自动注册完成: ${NODE_ID} (${NODE_NAME})"
