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
			continue
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
