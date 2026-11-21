package main

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"netwatch/internal/config"
	"netwatch/internal/model"
	"netwatch/internal/probe"
	"netwatch/internal/service"
	"netwatch/internal/store"
)

func authorizedRequest(r *http.Request, expectedToken string) bool {
	if expectedToken == "" {
		return true
	}
	candidate := strings.TrimSpace(r.Header.Get("X-Agent-Token"))
	if candidate == "" {
		auth := strings.TrimSpace(r.Header.Get("Authorization"))
		if len(auth) > 7 && strings.EqualFold(auth[:7], "Bearer ") {
			candidate = strings.TrimSpace(auth[7:])
		}
	}
	if candidate == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(candidate), []byte(expectedToken)) == 1
}

func newMasterMux(ctx context.Context, cfg *config.Config, st *store.Store, agg *service.Aggregator) *http.ServeMux {
	var knownMu sync.Mutex
	knownNodes := make(map[string]bool)
	for _, nd := range agg.ActiveNodes() {
		knownNodes[nd.ID] = true
	}

	mux := http.NewServeMux()

	// HTTP endpoint to receive Agent reports (auto-registers new nodes)
	mux.HandleFunc("/api/v1/report", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if cfg.AgentToken != "" && r.Header.Get("X-Agent-Token") != cfg.AgentToken {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		var rep model.AgentReport
		if err := json.NewDecoder(r.Body).Decode(&rep); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		retired, err := st.IsNodeRetired(rep.NodeID)
		if err != nil {
			http.Error(w, "node status unavailable", http.StatusInternalServerError)
			return
		}
		if retired {
			http.Error(w, "node retired", http.StatusForbidden)
			return
		}
		knownMu.Lock()
		if rep.NodeID != "" && !knownNodes[rep.NodeID] {
			meta := model.NodeDef{ID: rep.NodeID, Name: rep.NodeID, ResetDay: 1}
			if rep.NodeMeta != nil {
				meta = *rep.NodeMeta
				if meta.ID == "" {
					meta.ID = rep.NodeID
				}
			}
			if meta.Host == "" {
				meta.Host = clientIPFromReq(r)
			}
			if err := st.UpsertNode(meta, false); err == nil {
				knownNodes[rep.NodeID] = true
				log.Printf("auto-registered new node: %s (%s:%d)", meta.ID, meta.Host, meta.Port)
				if meta.Host != "" {
					go func(m model.NodeDef) {
						pCtx, cancel := context.WithTimeout(ctx, 25*time.Second)
						res, err := probe.MeasureGuangdong(pCtx, m.Host, m.Port, m.ProbePort, m.Protocol, cfg.GlobalpingToken)
						cancel()
						if err == nil && len(res) > 0 {
							_ = st.SaveProbes(m.ID, res)
						}
					}(meta)
				}
			}
		}
		knownMu.Unlock()
		if err := st.IngestReport(rep); err != nil {
			if errors.Is(err, store.ErrNodeRetired) {
				http.Error(w, "node retired", http.StatusForbidden)
				return
			}
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		respPayload := model.ReportResponse{OK: true}
		if syncCfg, md5Sum := config.ResolveAgentSyncConfig(cfg, rep.NodeID); syncCfg != nil && md5Sum != "" {
			respPayload.ConfigMD5 = md5Sum
			if r.Header.Get("X-Agent-Config-MD5") != md5Sum {
				respPayload.AgentConfig = syncCfg
			}
		}
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(respPayload)
	})

	// Read-only JSON endpoint for dashboard overview
	mux.HandleFunc("/api/v1/overview", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if !authorizedRequest(r, cfg.AgentToken) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		period := strings.TrimSpace(r.URL.Query().Get("period"))
		switch period {
		case "today", "week", "month":
		default:
			period = "month"
		}
		view, err := agg.BuildOverview(period)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(view)
	})

	// Read-only JSON endpoint for single node detail
	mux.HandleFunc("/api/v1/nodes/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if !authorizedRequest(r, cfg.AgentToken) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		rawID := strings.TrimPrefix(r.URL.Path, "/api/v1/nodes/")
		if rawID == "" || strings.Contains(rawID, "/") {
			http.Error(w, "invalid node id", http.StatusBadRequest)
			return
		}
		nodeID, err := url.PathUnescape(rawID)
		if err != nil || strings.TrimSpace(nodeID) == "" {
			http.Error(w, "invalid node id", http.StatusBadRequest)
			return
		}
		exists := false
		for _, nd := range agg.ActiveNodes() {
			if nd.ID == nodeID {
				exists = true
				break
			}
		}
		if !exists {
			http.Error(w, "node not found", http.StatusNotFound)
			return
		}
		window := strings.TrimSpace(r.URL.Query().Get("window"))
		if window == "" {
			window = "24h"
		}
		detail, err := agg.BuildNodeDetail(nodeID, window)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(detail)
	})

	return mux
}
