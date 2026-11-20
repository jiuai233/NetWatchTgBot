package render

import (
	"strings"
	"testing"
	"time"

	"netwatch/internal/model"
)

func TestNodeStatusDistinguishesAgentOnlineAndProxyBlocked(t *testing.T) {
	view := model.OverviewView{
		Title:       "NetWatch",
		PeriodLabel: "本月",
		GeneratedAt: time.Now(),
		Nodes: []model.NodeSummary{
			{ID: "n-ok", Name: "142", Host: "1.1.1.1", Online: true, ProxyState: "ok", GDLatencyMs: 108},
			{ID: "n-down", Name: "144", Host: "2.2.2.2", Online: true, ProxyState: "down", GDLatencyMs: 0, GDLossPct: 100, GDProbeCount: 5, GDOkCount: 0},
			{ID: "n-slow", Name: "154", Host: "3.3.3.3", Online: true, ProxyState: "slow", GDLatencyMs: 199, GDLossPct: 25},
			{ID: "n-unk", Name: "new", Host: "4.4.4.4", Online: true, ProxyState: "unknown"},
			{ID: "n-off", Name: "off", Host: "5.5.5.5", Online: false},
		},
	}

	txt := RenderOverviewText(view)
	for _, want := range []string{
		"🟢 广东 <code>108ms</code>",
		"🔴 广东 <code>不可用 (Agent在线·广东阻断)</code>",
		"🟡 广东 <code>199ms (丢包25%)</code>",
		"⚪ 广东 <code>待测 (仅Agent在线)</code>",
		"🔴 广东 <code>Agent 离线</code>",
	} {
		if !strings.Contains(txt, want) {
			t.Fatalf("expected %q in rendered text:\n%s", want, txt)
		}
	}
}
