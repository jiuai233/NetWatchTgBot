package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"netwatch/internal/config"
	"netwatch/internal/model"
	"netwatch/internal/service"
	"netwatch/internal/store"
)

func setupTestMasterHandler(t *testing.T) http.Handler {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "test.db")
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	cfg := &config.Config{
		AgentToken: "top-secret",
		Nodes: []model.NodeDef{
			{
				ID:           "vps-sg",
				Name:         "新加坡节点",
				Host:         "203.0.113.20",
				Port:         57423,
				Protocol:     "VLESS",
				QuotaGB:      800,
				ResetDay:     26,
				ExpireDate:   "2026-12-26",
				AutoRenew:    true,
				BillingCycle: "monthly",
			},
		},
	}
	agg := service.NewAggregator(cfg, st)
	if err := agg.SyncConfigNodes(); err != nil {
		t.Fatalf("sync nodes: %v", err)
	}
	now := time.Now()
	if err := st.IngestReport(model.AgentReport{
		NodeID:      "vps-sg",
		Timestamp:   now,
		CPUPct:      12.5,
		MemPct:      30.0,
		DiskPct:     20.0,
		NetRxTotal:  1024 * 1024 * 500,
		NetTxTotal:  1024 * 1024 * 250,
		RxBps:       204800,
		TxBps:       102400,
		ActiveConns: 9,
		Probes: []model.ProbeEntry{
			{Name: "广东广州 · 电信", Tag: "gd", LatencyMs: 48.5, LossPct: 0, Status: "ok", UpdatedAt: now},
		},
	}); err != nil {
		t.Fatalf("ingest report: %v", err)
	}
	return newMasterMux(context.Background(), cfg, st, agg)
}

func TestReadOnlyAPIsAuthAndResponses(t *testing.T) {
	h := setupTestMasterHandler(t)

	// 1. 缺 Token 应返回 401
	req := httptest.NewRequest(http.MethodGet, "/api/v1/overview?period=month", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 without token, got %d", rec.Code)
	}

	// 2. 非 GET 方法返回 405
	req = httptest.NewRequest(http.MethodPost, "/api/v1/overview", nil)
	req.Header.Set("X-Agent-Token", "top-secret")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405 for POST, got %d", rec.Code)
	}

	// 3. X-Agent-Token 正常拉取总览并携带 AutoRenew/BillingCycle
	req = httptest.NewRequest(http.MethodGet, "/api/v1/overview?period=month", nil)
	req.Header.Set("X-Agent-Token", "top-secret")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 overview, got %d: %s", rec.Code, rec.Body.String())
	}
	var ov model.OverviewView
	if err := json.Unmarshal(rec.Body.Bytes(), &ov); err != nil {
		t.Fatalf("decode overview: %v", err)
	}
	if len(ov.Nodes) != 1 || ov.Nodes[0].ID != "vps-sg" || !ov.Nodes[0].AutoRenew || ov.Nodes[0].BillingCycle != "monthly" {
		t.Fatalf("unexpected overview nodes: %+v", ov.Nodes)
	}

	// 4. Bearer Token 正常拉取单节点详情
	req = httptest.NewRequest(http.MethodGet, "/api/v1/nodes/vps-sg?window=24h", nil)
	req.Header.Set("Authorization", "Bearer top-secret")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 node detail, got %d: %s", rec.Code, rec.Body.String())
	}
	var det model.NodeDetailView
	if err := json.Unmarshal(rec.Body.Bytes(), &det); err != nil {
		t.Fatalf("decode detail: %v", err)
	}
	if det.Node.ID != "vps-sg" || len(det.Probes) != 1 || det.Probes[0].Name != "广东广州 · 电信" {
		t.Fatalf("unexpected detail: %+v", det)
	}

	// 5. 不存在的节点返回 404
	req = httptest.NewRequest(http.MethodGet, "/api/v1/nodes/does-not-exist", nil)
	req.Header.Set("X-Agent-Token", "top-secret")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for unknown node, got %d", rec.Code)
	}
}
