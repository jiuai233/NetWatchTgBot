# NetWatchTgBot

基于 Go 与 SQLite 的 Telegram Bot 多节点探针面板，支持在聊天窗口内直接查看卡片图表或分段文字报表、广东三网 TCP/ICMP 延迟检测、客户端连接 IP 归属地与持久化流量统计，以及新节点自动注册。

## 主要特性

- **Telegram 原生交互面板**：支持卡片 PNG 图表与分段纯文字双模式切换、Inline 交互按钮与底部快捷键盘，统一展示为 UTC+8 时区。
- **持久化防重启归零**：网卡累计字节与客户端 IP 消耗采用单调增量累加写入 SQLite，系统或代理进程重启均不丢失历史统计。
- **分节点独立配额与热校准**：支持按节点独立设置月配额、重置日与到期日，并提供 `/set <节点ID> <已用GB>` 热校准命令。
- **广东三网连通性探测**：集成 Globalping 广州/深圳多运营商探针进行端口 TCPing（纯 UDP 协议或指定 `probe_port` 自动适配）。
- **在线 IP 归属与速率**：结合 `/proc/net/tcp*`、`/proc/net/udp*` 与 `sing-box` Clash API 统计各节点当前连入 IP、归属地运营商、实时速率与累计流量。
- **新节点自动注册**：Agent 携带注册元数据连接 Master 后自动写入数据库并出现在 Telegram 菜单与看板中，无需重启 Master。

## 快速使用

详细编译、Release 发布、新机器一键接入与主控迁移步骤见 [DEPLOY.md](./DEPLOY.md)。
