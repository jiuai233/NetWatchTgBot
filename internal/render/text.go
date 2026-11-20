package render

import (
	"fmt"
	"strings"
	"time"

	"netwatch/internal/collector"
	"netwatch/internal/model"
)

var cstZone = time.FixedZone("CST", 8*3600)

func toCST(t time.Time) time.Time {
	return t.In(cstZone)
}

func RenderOverviewText(v model.OverviewView) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "📊 <b>%s</b>（%s）· 实时总速 <code>%s</code>\n\n",
		esc(v.Title), esc(v.PeriodLabel), formatBps(v.CurrentBps))

	// Group clients by NodeID
	byNode := make(map[string][]model.ClientIPStat)
	for _, c := range v.TopClients {
		byNode[c.NodeID] = append(byNode[c.NodeID], c)
	}

	for _, n := range v.Nodes {
		icon, lat := nodeStatusBadge(n)

		fmt.Fprintf(&sb, "━━ <b>%s</b> (<code>%s</code>) ━━\n", esc(n.Name), esc(n.Host))
		fmt.Fprintf(&sb, "%s 广东 <code>%s</code> · %d连接 · 实时 <code>%s</code>\n",
			icon, lat, n.ActiveConns, formatBps(n.RxBps+n.TxBps))

		if n.QuotaBytes > 0 {
			pct := float64(n.UsedBytes) / float64(n.QuotaBytes) * 100
			rem := n.QuotaBytes - n.UsedBytes
			if rem < 0 {
				rem = 0
			}
			fmt.Fprintf(&sb, "套餐：<code>%s</code> / <code>%s</code>（%.1f%% · 剩 %s）\n",
				collector.FormatBytes(n.UsedBytes), collector.FormatBytes(n.QuotaBytes), pct, collector.FormatBytes(rem))
			exp := n.ExpireDate
			if exp == "" {
				exp = "--"
			}
			fmt.Fprintf(&sb, "%s · 每月%d日重置 · 到期 %s\n", textBar(pct, 10), n.ResetDay, esc(exp))
		} else {
			fmt.Fprintf(&sb, "用量：<code>%s</code>\n", collector.FormatBytes(n.UsedBytes))
		}

		clients := byNode[n.ID]
		if len(clients) > 0 {
			sb.WriteString("连接 IP：\n")
			limit := len(clients)
			if limit > 5 {
				limit = 5
			}
			for _, c := range clients[:limit] {
				reg := c.Region
				if reg == "" {
					reg = "未知"
				}
				st := "历史"
				if c.ActiveConns > 0 {
					st = fmt.Sprintf("%d连", c.ActiveConns)
				}
				fmt.Fprintf(&sb, "  • <code>%s</code> (%s) · %s · %s · <b>%s</b>\n",
					esc(c.IP), esc(reg), st, formatBps(c.RxBps+c.TxBps), collector.FormatBytes(c.TotalRx+c.TotalTx))
			}
		} else {
			sb.WriteString("连接 IP：暂无活跃连接\n")
		}
		sb.WriteString("\n")
	}

	fmt.Fprintf(&sb, "<i>更新于 %s UTC+8（重启不丢统计）</i>", toCST(v.GeneratedAt).Format("15:04:05"))
	return sb.String()
}

func nodeStatusBadge(n model.NodeSummary) (icon, lat string) {
	if !n.Online {
		return "🔴", "Agent 离线"
	}
	switch n.ProxyState {
	case "down":
		return "🔴", "不可用 (Agent在线·广东阻断)"
	case "slow":
		if n.GDLossPct > 10 {
			return "🟡", fmt.Sprintf("%.0fms (丢包%.0f%%)", n.GDLatencyMs, n.GDLossPct)
		}
		return "🟡", fmt.Sprintf("%.0fms", n.GDLatencyMs)
	case "ok":
		return "🟢", fmt.Sprintf("%.0fms", n.GDLatencyMs)
	default:
		if n.GDProbeCount > 0 && n.GDOkCount == 0 {
			return "🔴", "不可用 (Agent在线·广东阻断)"
		}
		if n.GDLatencyMs > 0 {
			if n.GDLatencyMs > 220 || n.GDLossPct > 10 {
				return "🟡", fmt.Sprintf("%.0fms", n.GDLatencyMs)
			}
			return "🟢", fmt.Sprintf("%.0fms", n.GDLatencyMs)
		}
		return "⚪", "待测 (仅Agent在线)"
	}
}

func RenderNodeDetailText(v model.NodeDetailView) string {
	var sb strings.Builder
	icon, lat := nodeStatusBadge(v.Node)

	fmt.Fprintf(&sb, "%s <b>%s</b>（<code>%s:%d</code> · %s）\n", icon, esc(v.Node.Name), esc(v.Node.Host), v.Node.Port, esc(v.Node.Protocol))
	fmt.Fprintf(&sb, "健康度：<b>%d</b> · %s（%s）\n", v.Node.HealthScore, esc(v.Node.HealthLabel), esc(v.Node.HealthNote))
	fmt.Fprintf(&sb, "CPU <code>%.0f%%</code> | 内存 <code>%.0f%%</code> | 硬盘 <code>%.0f%%</code> | 广东延迟 <code>%s</code>\n",
		v.Node.CPUPct, v.Node.MemPct, v.Node.DiskPct, lat)
	fmt.Fprintf(&sb, "实时速率：<code>%s</code> | 活跃连接：<code>%d</code>\n",
		formatBps(v.Node.RxBps+v.Node.TxBps), v.Node.ActiveConns)
	if v.Node.QuotaBytes > 0 {
		pct := float64(v.Node.UsedBytes) / float64(v.Node.QuotaBytes) * 100
		fmt.Fprintf(&sb, "本周期：<code>%s</code> / <code>%s</code>（%.1f%% · 每月%d日重置）\n",
			collector.FormatBytes(v.Node.UsedBytes), collector.FormatBytes(v.Node.QuotaBytes), pct, v.Node.ResetDay)
	}
	sb.WriteString("\n")

	sb.WriteString("<b>最近日流量（持久化累计）</b>\n")
	for _, d := range v.Daily7 {
		label := d.Date
		if len(label) >= 10 {
			label = label[5:]
		}
		fmt.Fprintf(&sb, "<code>%s</code>  总 <b>%s</b>（↑%s / ↓%s）\n",
			label, collector.FormatBytes(d.RxBytes+d.TxBytes), collector.FormatBytes(d.TxBytes), collector.FormatBytes(d.RxBytes))
	}

	if len(v.Probes) > 0 {
		sb.WriteString("\n<b>探测地址</b>\n")
		for _, p := range v.Probes {
			if p.Status == "down" || p.LatencyMs <= 0 {
				fmt.Fprintf(&sb, "• %s：<code>超时</code>（丢包 100%%）\n", esc(p.Name))
			} else {
				fmt.Fprintf(&sb, "• %s：<code>%.0f ms</code>（丢包 %.1f%%）\n", esc(p.Name), p.LatencyMs, p.LossPct)
			}
		}
	}

	if len(v.ClientIPs) > 0 {
		sb.WriteString("\n<b>当前连接 IP 与流量排行</b>\n")
		for _, c := range v.ClientIPs {
			st := "离线"
			if c.ActiveConns > 0 {
				st = fmt.Sprintf("在线 %d连接", c.ActiveConns)
			}
			reg := c.Region
			if reg == "" {
				reg = "未知地区"
			}
			fmt.Fprintf(&sb, "• <code>%s</code> (%s) | %s | 实时 %s | 累计 <b>%s</b>\n",
				esc(c.IP), esc(reg), st, formatBps(c.RxBps+c.TxBps), collector.FormatBytes(c.TotalRx+c.TotalTx))
		}
	}

	fmt.Fprintf(&sb, "\n<i>窗口: %s · 更新于 %s UTC+8</i>", esc(v.WindowLabel), toCST(v.GeneratedAt).Format("15:04:05"))
	return sb.String()
}

func RenderClientsOnlyText(list []model.ClientIPStat) string {
	if len(list) == 0 {
		return "当前暂无记录到的客户端连接 IP。"
	}
	var sb strings.Builder
	sb.WriteString("🌐 <b>各 VPS 连接 IP 与流量消耗明细</b>\n\n")

	order := make([]string, 0)
	grouped := make(map[string][]model.ClientIPStat)
	for _, c := range list {
		key := c.NodeName
		if key == "" {
			key = c.NodeID
		}
		if _, ok := grouped[key]; !ok {
			order = append(order, key)
		}
		grouped[key] = append(grouped[key], c)
	}

	for _, node := range order {
		fmt.Fprintf(&sb, "━━ <b>%s</b> ━━\n", esc(node))
		for _, c := range grouped[node] {
			st := "⚪ 历史"
			if c.ActiveConns > 0 {
				st = fmt.Sprintf("🟢 %d连接", c.ActiveConns)
			}
			reg := c.Region
			if reg == "" {
				reg = "未知地区"
			}
			fmt.Fprintf(&sb, "• <code>%s</code> (%s)\n  %s · 实时 %s · 累计 <b>%s</b> (↓%s / ↑%s)\n",
				esc(c.IP), esc(reg), st, formatBps(c.RxBps+c.TxBps),
				collector.FormatBytes(c.TotalRx+c.TotalTx),
				collector.FormatBytes(c.TotalRx), collector.FormatBytes(c.TotalTx))
		}
		sb.WriteString("\n")
	}
	sb.WriteString("<i>说明：IP 消耗按增量持久化，重启不归零；无 Clash API 的节点仅记录活跃连接与归属地。</i>")
	return sb.String()
}

func textBar(pct float64, width int) string {
	if pct < 0 {
		pct = 0
	}
	if pct > 100 {
		pct = 100
	}
	filled := int(pct / 100 * float64(width))
	if filled > width {
		filled = width
	}
	return "[" + strings.Repeat("█", filled) + strings.Repeat("░", width-filled) + "]"
}

func esc(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	return s
}
