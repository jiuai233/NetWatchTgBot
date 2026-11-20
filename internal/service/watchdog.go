package service

import (
	"context"
	"fmt"
	"net"
	"strings"
	"time"

	"netwatch/internal/model"
	"netwatch/internal/store"
)

type AlertSender func(text string)

type nodeWatchState struct {
	failStreak  int
	okStreak    int
	alerting    bool
	reason      string
	downSince   time.Time
	lastAlertAt time.Time
}

type Watchdog struct {
	agg      *Aggregator
	store    *store.Store
	send     AlertSender
	states   map[string]*nodeWatchState
	cst      *time.Location
	cooldown time.Duration
}

func NewWatchdog(agg *Aggregator, st *store.Store, send AlertSender) *Watchdog {
	return &Watchdog{
		agg:      agg,
		store:    st,
		send:     send,
		states:   make(map[string]*nodeWatchState),
		cst:      time.FixedZone("CST", 8*3600),
		cooldown: 30 * time.Minute,
	}
}

func (w *Watchdog) Run(ctx context.Context) {
	// Wait on startup so agents have time to check in
	select {
	case <-ctx.Done():
		return
	case <-time.After(60 * time.Second):
	}

	w.CheckAll()
	tk := time.NewTicker(60 * time.Second)
	defer tk.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tk.C:
			w.CheckAll()
		}
	}
}

func (w *Watchdog) CheckAll() {
	now := time.Now()
	for _, nd := range w.agg.ActiveNodes() {
		w.checkNodeHealth(nd, now)
		w.checkNodeQuota(nd, now)
		w.checkNodeExpiry(nd, now)
	}
}

func (w *Watchdog) checkNodeHealth(nd model.NodeDef, now time.Time) {
	st, ok := w.states[nd.ID]
	if !ok {
		st = &nodeWatchState{}
		w.states[nd.ID] = st
	}

	reason := w.inspectNode(nd, now)
	if reason != "" {
		st.failStreak++
		st.okStreak = 0
		// Require 3 consecutive failed checks (~3 min) and respect 30m per-node cooldown
		if st.failStreak >= 3 && !st.alerting {
			st.alerting = true
			st.reason = reason
			st.downSince = now
			if st.lastAlertAt.IsZero() || now.Sub(st.lastAlertAt) >= w.cooldown {
				st.lastAlertAt = now
				w.send(fmt.Sprintf(
					"🚨 <b>节点异常告警</b>\n节点：<b>%s</b> (<code>%s:%d</code>)\n原因：%s\n时间：%s UTC+8",
					escHTML(nd.Name), escHTML(nd.Host), nd.Port, escHTML(reason), now.In(w.cst).Format("2006-01-02 15:04:05"),
				))
			}
		} else if st.alerting {
			st.reason = reason
		}
		return
	}

	// Healthy check
	st.failStreak = 0
	if st.alerting {
		st.okStreak++
		// Require 2 consecutive healthy checks (~2 min) before clearing alert
		if st.okStreak >= 2 {
			dur := now.Sub(st.downSince).Round(time.Second)
			st.alerting = false
			st.okStreak = 0
			if !st.lastAlertAt.IsZero() {
				w.send(fmt.Sprintf(
					"✅ <b>节点已恢复正常</b>\n节点：<b>%s</b> (<code>%s:%d</code>)\n此前异常：%s\n持续时长：%s\n时间：%s UTC+8",
					escHTML(nd.Name), escHTML(nd.Host), nd.Port, escHTML(st.reason), dur.String(), now.In(w.cst).Format("2006-01-02 15:04:05"),
				))
			}
			st.reason = ""
		}
	}
}

var quotaThresholds = []int{100, 90, 80}

func (w *Watchdog) checkNodeQuota(nd model.NodeDef, now time.Time) {
	if nd.QuotaGB <= 0 {
		return
	}
	nowCST := now.In(w.cst)
	cycStart := billingCycleStart(nowCST, nd.ResetDay)
	rx, tx, err := w.store.GetTrafficSince(nd.ID, cycStart)
	if err != nil {
		return
	}
	calBase := w.store.GetCycleCalibration(nd.ID, cycStart)
	usedBytes := rx + tx + calBase
	if usedBytes < 0 {
		usedBytes = 0
	}
	quotaBytes := nd.QuotaGB * 1024 * 1024 * 1024
	pct := float64(usedBytes) / float64(quotaBytes) * 100.0

	hit := 0
	for _, th := range quotaThresholds {
		if pct >= float64(th) {
			hit = th
			break
		}
	}
	if hit == 0 {
		return
	}
	kind := fmt.Sprintf("quota_%d", hit)
	if w.store.HasAlertEvent(nd.ID, kind, cycStart) {
		return
	}
	for _, th := range quotaThresholds {
		if th <= hit {
			_ = w.store.MarkAlertEvent(nd.ID, fmt.Sprintf("quota_%d", th), cycStart)
		}
	}
	usedGB := float64(usedBytes) / (1024 * 1024 * 1024)
	resetDay := nd.ResetDay
	if resetDay <= 0 || resetDay > 28 {
		resetDay = 1
	}
	w.send(fmt.Sprintf(
		"⚠️ <b>月流量配额预警 (%d%%)</b>\n节点：<b>%s</b>\n本周期已用：<code>%.2f GB / %d GB (%.1f%%)</code>\n周期起点：%s（每月 %d 日重置）\n时间：%s UTC+8",
		hit, escHTML(nd.Name), usedGB, nd.QuotaGB, pct, cycStart, resetDay, nowCST.Format("2006-01-02 15:04:05"),
	))
}

func (w *Watchdog) checkNodeExpiry(nd model.NodeDef, now time.Time) {
	if strings.TrimSpace(nd.ExpireDate) == "" {
		return
	}
	expDate, err := time.ParseInLocation("2006-01-02", strings.TrimSpace(nd.ExpireDate), w.cst)
	if err != nil {
		return
	}
	nowCST := now.In(w.cst)
	today := time.Date(nowCST.Year(), nowCST.Month(), nowCST.Day(), 0, 0, 0, 0, w.cst)
	daysLeft := int(expDate.Sub(today).Hours() / 24)

	if daysLeft < 0 {
		if nd.AutoRenew || strings.TrimSpace(nd.BillingCycle) != "" {
			nextDate := advanceExpireDate(expDate, today, nd.BillingCycle)
			nextStr := nextDate.Format("2006-01-02")
			if err := w.store.UpdateNodeExpireDate(nd.ID, nextStr); err == nil && !w.store.HasAlertEvent(nd.ID, "expire_renewed", nd.ExpireDate) {
				_ = w.store.MarkAlertEvent(nd.ID, "expire_renewed", nd.ExpireDate)
				w.send(fmt.Sprintf(
					"🔄 <b>节点到期日已自动顺延</b>\n节点：<b>%s</b>\n原到期日：<code>%s</code>\n新到期日：<code>%s</code>\n时间：%s UTC+8",
					escHTML(nd.Name), escHTML(nd.ExpireDate), nextStr, nowCST.Format("2006-01-02 15:04:05"),
				))
			}
			return
		}
		if !w.store.HasAlertEvent(nd.ID, "expire_expired", nd.ExpireDate) {
			_ = w.store.MarkAlertEvent(nd.ID, "expire_expired", nd.ExpireDate)
			w.send(fmt.Sprintf(
				"⏰ <b>节点已到期提醒</b>\n节点：<b>%s</b>\n到期日：<code>%s</code>（已过期 %d 天）\n时间：%s UTC+8",
				escHTML(nd.Name), escHTML(nd.ExpireDate), -daysLeft, nowCST.Format("2006-01-02 15:04:05"),
			))
		}
		return
	}

	for _, d := range []int{7, 3, 1, 0} {
		if daysLeft == d {
			kind := fmt.Sprintf("expire_%dd", d)
			if !w.store.HasAlertEvent(nd.ID, kind, nd.ExpireDate) {
				_ = w.store.MarkAlertEvent(nd.ID, kind, nd.ExpireDate)
				desc := fmt.Sprintf("还有 <b>%d</b> 天到期", d)
				if d == 0 {
					desc = "<b>今天到期</b>"
				}
				w.send(fmt.Sprintf(
					"📅 <b>节点续费提醒</b>\n节点：<b>%s</b>\n状态：%s（到期日 <code>%s</code>）\n时间：%s UTC+8",
					escHTML(nd.Name), desc, escHTML(nd.ExpireDate), nowCST.Format("2006-01-02 15:04:05"),
				))
			}
			break
		}
	}
}

func advanceExpireDate(expDate, today time.Time, cycle string) time.Time {
	c := strings.ToLower(strings.TrimSpace(cycle))
	next := expDate
	for !next.After(today) {
		if c == "yearly" || c == "year" || c == "annual" {
			next = next.AddDate(1, 0, 0)
		} else {
			next = next.AddDate(0, 1, 0)
		}
	}
	return next
}

func (w *Watchdog) inspectNode(nd model.NodeDef, now time.Time) string {
	var issues []string

	// 1. Agent heartbeat check
	sample, hasSample := w.store.GetLatestSample(nd.ID)
	if !hasSample {
		issues = append(issues, "Agent 尚未上报心跳")
	} else if now.Sub(sample.Timestamp) > 180*time.Second {
		ago := now.Sub(sample.Timestamp).Round(time.Second)
		issues = append(issues, fmt.Sprintf("Agent 心跳失联 (%s 无数据)", ago))
	} else {
		if sample.DiskPct >= 95 {
			issues = append(issues, fmt.Sprintf("磁盘空间告警 (%.0f%%)", sample.DiskPct))
		}
		if sample.MemPct >= 97 {
			issues = append(issues, fmt.Sprintf("内存占满告警 (%.0f%%)", sample.MemPct))
		}
	}

	// 2. TCP proxy port reachability check
	checkPort := nd.ProbePort
	if checkPort <= 0 {
		low := strings.ToLower(nd.Protocol)
		if !strings.Contains(low, "hysteria") && !strings.Contains(low, "tuic") && !strings.Contains(low, "wireguard") {
			checkPort = nd.Port
		}
	}
	if nd.Host != "" && checkPort > 0 {
		if !tcpProbe(nd.Host, checkPort) {
			issues = append(issues, fmt.Sprintf("端口 %d TCP 连接超时", checkPort))
		}
	}

	// 3. Check Guangdong Globalping probe reachability (distinguish Agent online vs proxy reachable from CN)
	if probes, err := w.store.GetProbes(nd.ID); err == nil {
		gdTotal, gdOK := 0, 0
		var newest time.Time
		for _, p := range probes {
			if p.Tag == "gd" {
				gdTotal++
				if p.Status != "down" && p.LatencyMs > 0 {
					gdOK++
				}
				if p.UpdatedAt.After(newest) {
					newest = p.UpdatedAt
				}
			}
		}
		if gdTotal >= 2 && gdOK == 0 && !newest.IsZero() && now.Sub(newest) <= 90*time.Minute {
			issues = append(issues, "Agent 在线，但广东探测全超时/阻断")
		}
	}

	return strings.Join(issues, "；")
}

func tcpProbe(host string, port int) bool {
	addr := net.JoinHostPort(host, fmt.Sprintf("%d", port))
	for i := 0; i < 2; i++ {
		c, err := net.DialTimeout("tcp", addr, 4*time.Second)
		if err == nil {
			c.Close()
			return true
		}
		time.Sleep(500 * time.Millisecond)
	}
	return false
}

func escHTML(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	return s
}
