package probe

import (
	"testing"
	"time"

	"netwatch/internal/model"
)

func TestRollingWindowMedianAndLoss(t *testing.T) {
	rw := NewRollingWindow()
	seq := []struct {
		ms float64
		ok bool
	}{
		{40, true},
		{500, true}, // transient spike
		{0, false},  // 1 packet lost
		{44, true},
	}
	idx := 0
	rw.dialFn = func(addr string, timeout time.Duration) (float64, bool) {
		cur := seq[idx]
		idx++
		return cur.ms, cur.ok
	}

	targets := []model.TargetDef{{Name: "CT", Tag: "gd", Addr: "1.2.3.4:80"}}
	base := time.Date(2026, 11, 20, 15, 0, 0, 0, time.UTC)
	for i := 0; i < len(seq); i++ {
		rw.ProbeAll(targets, base.Add(time.Duration(i)*20*time.Second))
	}

	snap := rw.Snapshot(targets, base.Add(60*time.Second))
	if len(snap) != 1 {
		t.Fatalf("expected 1 probe entry, got %d", len(snap))
	}
	// Successful RTTs: [40, 44, 500] -> median is 44ms; 1 of 4 lost -> 25%
	if snap[0].LatencyMs != 44 {
		t.Fatalf("expected median 44ms, got %.2f", snap[0].LatencyMs)
	}
	if snap[0].LossPct != 25 {
		t.Fatalf("expected loss 25%%, got %.2f", snap[0].LossPct)
	}
	if snap[0].Status != "slow" {
		t.Fatalf("expected slow due to 25%% loss, got %s", snap[0].Status)
	}

	// Samples older than 2 minutes are evicted
	oldSnap := rw.Snapshot(targets, base.Add(5*time.Minute))
	if len(oldSnap) != 0 {
		t.Fatalf("expected expired window to return 0 entries, got %d", len(oldSnap))
	}
}
