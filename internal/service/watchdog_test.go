package service

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"netwatch/internal/config"
	"netwatch/internal/model"
	"netwatch/internal/store"
)

func newTestWatchdog(t *testing.T) (*Watchdog, *store.Store, *[]string) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	agg := NewAggregator(&config.Config{}, st)
	var msgs []string
	w := NewWatchdog(agg, st, func(text string) {
		msgs = append(msgs, text)
	})
	return w, st, &msgs
}

func TestWatchdogQuotaTieredAlertsNoSpam(t *testing.T) {
	w, st, msgs := newTestWatchdog(t)
	nd := model.NodeDef{ID: "n1", Name: "日本节点", QuotaGB: 100, ResetDay: 1}
	if err := st.UpsertNode(nd, false); err != nil {
		t.Fatal(err)
	}

	now := time.Date(2026, 11, 20, 12, 0, 0, 0, w.cst)
	cycStart := billingCycleStart(now, 1)

	// 82% -> triggers quota_80 once
	if err := st.SetCycleCalibration("n1", cycStart, 82*1024*1024*1024); err != nil {
		t.Fatal(err)
	}
	w.checkNodeQuota(nd, now)
	w.checkNodeQuota(nd, now.Add(time.Minute))
	if len(*msgs) != 1 || !strings.Contains((*msgs)[0], "80%") {
		t.Fatalf("expected single 80%% alert, got: %v", *msgs)
	}

	// Jump to 105% -> triggers quota_100 once (skips duplicate 90% alert)
	if err := st.SetCycleCalibration("n1", cycStart, 105*1024*1024*1024); err != nil {
		t.Fatal(err)
	}
	w.checkNodeQuota(nd, now.Add(2*time.Minute))
	w.checkNodeQuota(nd, now.Add(3*time.Minute))
	if len(*msgs) != 2 || !strings.Contains((*msgs)[1], "100%") {
		t.Fatalf("expected 100%% alert next, got: %v", *msgs)
	}

	// Next billing cycle resets alert deduplication
	nextCycleNow := time.Date(2026, 12, 5, 12, 0, 0, 0, w.cst)
	nextCycStart := billingCycleStart(nextCycleNow, 1)
	if err := st.SetCycleCalibration("n1", nextCycStart, 92*1024*1024*1024); err != nil {
		t.Fatal(err)
	}
	w.checkNodeQuota(nd, nextCycleNow)
	if len(*msgs) != 3 || !strings.Contains((*msgs)[2], "90%") {
		t.Fatalf("expected 90%% alert in new cycle, got: %v", *msgs)
	}
}

func TestWatchdogExpiryReminderAndAutoRenew(t *testing.T) {
	w, st, msgs := newTestWatchdog(t)
	nd := model.NodeDef{
		ID:           "n-renew",
		Name:         "续费机",
		ExpireDate:   "2026-11-23",
		AutoRenew:    true,
		BillingCycle: "monthly",
	}
	if err := st.UpsertNode(nd, false); err != nil {
		t.Fatal(err)
	}

	// 3 days left -> reminder once
	t3 := time.Date(2026, 11, 20, 10, 0, 0, 0, w.cst)
	w.checkNodeExpiry(nd, t3)
	w.checkNodeExpiry(nd, t3.Add(time.Hour))
	if len(*msgs) != 1 || !strings.Contains((*msgs)[0], "3") {
		t.Fatalf("expected 3-day reminder, got: %v", *msgs)
	}

	// Past expiry -> auto renew to 2026-12-23
	tExpired := time.Date(2026, 11, 24, 1, 0, 0, 0, w.cst)
	w.checkNodeExpiry(nd, tExpired)
	if len(*msgs) != 2 || !strings.Contains((*msgs)[1], "2026-12-23") {
		t.Fatalf("expected auto-renew to 2026-12-23, got: %v", *msgs)
	}

	// Verify DB persisted new date and UpsertNode(overwrite=true) with stale config keeps 2026-12-23
	if err := st.UpsertNode(nd, true); err != nil {
		t.Fatal(err)
	}
	nodes, err := st.ListNodes()
	if err != nil || len(nodes) != 1 || nodes[0].ExpireDate != "2026-12-23" {
		t.Fatalf("expected preserved expire_date 2026-12-23, got: %+v err=%v", nodes, err)
	}
}
