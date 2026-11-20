package store

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	"netwatch/internal/model"
)

func TestRetireNodePreservesHistoryAndBlocksReports(t *testing.T) {
	path := filepath.Join(t.TempDir(), "netwatch.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"retired", "active"} {
		if err := s.UpsertNode(model.NodeDef{ID: id}, false); err != nil {
			t.Fatal(err)
		}
		if err := s.IngestReport(model.AgentReport{NodeID: id, Timestamp: time.Now(), CPUPct: 7}); err != nil {
			t.Fatal(err)
		}
	}
	changed, err := s.RetireNode("retired")
	if err != nil || !changed {
		t.Fatalf("retire: %v %v", changed, err)
	}
	if err := s.IngestReport(model.AgentReport{NodeID: "retired", Timestamp: time.Now()}); !errors.Is(err, ErrNodeRetired) {
		t.Fatalf("retired report: %v", err)
	}
	if err := s.IngestReport(model.AgentReport{NodeID: "active", Timestamp: time.Now()}); err != nil {
		t.Fatal(err)
	}
	nodes, err := s.ListNodes()
	if err != nil || len(nodes) != 1 || nodes[0].ID != "active" {
		t.Fatalf("nodes: %v %v", nodes, err)
	}
	hist, err := s.GetResourceSamples("retired", time.Now().Add(-time.Hour))
	if err != nil || len(hist) != 1 {
		t.Fatalf("history: %v %v", hist, err)
	}
	s.Close()
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	retired, err := s.IsNodeRetired("retired")
	if err != nil || !retired {
		t.Fatalf("after restart: %v %v", retired, err)
	}
	if err := s.IngestReport(model.AgentReport{NodeID: "retired", Timestamp: time.Now()}); !errors.Is(err, ErrNodeRetired) {
		t.Fatalf("after restart report: %v", err)
	}
}

func TestOutOfOrderReportDoesNotRegressMonotonicCounter(t *testing.T) {
	path := filepath.Join(t.TempDir(), "delta.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	t1 := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	t2 := t1.Add(60 * time.Second)
	tOld := t1.Add(-30 * time.Second)

	if err := s.IngestReport(model.AgentReport{NodeID: "n1", Timestamp: t1, NetRxTotal: 1000, NetTxTotal: 2000}); err != nil {
		t.Fatal(err)
	}
	if err := s.IngestReport(model.AgentReport{NodeID: "n1", Timestamp: t2, NetRxTotal: 1500, NetTxTotal: 2600}); err != nil {
		t.Fatal(err)
	}
	// Out-of-order older packet must not reset counter_state
	if err := s.IngestReport(model.AgentReport{NodeID: "n1", Timestamp: tOld, NetRxTotal: 800, NetTxTotal: 1200}); err != nil {
		t.Fatal(err)
	}

	rx, tx, err := s.GetTrafficSince("n1", "2026-11-20")
	if err != nil {
		t.Fatal(err)
	}
	if rx != 500 || tx != 600 {
		t.Fatalf("expected rx=500 tx=600, got rx=%d tx=%d", rx, tx)
	}
}
