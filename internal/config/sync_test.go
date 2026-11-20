package config

import (
	"path/filepath"
	"testing"

	"netwatch/internal/model"
)

func TestResolveAgentSyncConfigAndSaveAtomic(t *testing.T) {
	autoUpd := true
	proxy := "https://ghfast.top/"
	cfg := &Config{
		AgentDefaults: &model.AgentSyncConfig{
			IntervalSec: 45,
			AutoUpdate:  &autoUpd,
			UpdateProxy: &proxy,
			LocalTargets: []model.TargetDef{
				{Name: "CF", Tag: "intl", Addr: "1.1.1.1:443"},
			},
		},
		AgentOverrides: map[string]model.AgentSyncConfig{
			"vps-jp": {
				IntervalSec: 15,
				WatchPorts:  []int{443, 8443},
			},
		},
	}

	syncJP, md5JP := ResolveAgentSyncConfig(cfg, "vps-jp")
	if syncJP == nil || md5JP == "" {
		t.Fatal("expected resolved config and md5")
	}
	if syncJP.IntervalSec != 15 || len(syncJP.WatchPorts) != 2 || len(syncJP.LocalTargets) != 1 {
		t.Fatalf("unexpected merged config: %+v", syncJP)
	}

	syncOther, md5Other := ResolveAgentSyncConfig(cfg, "vps-sg")
	if syncOther == nil || syncOther.IntervalSec != 45 || md5Other == md5JP {
		t.Fatalf("expected default config with distinct md5, got: %+v md5=%s", syncOther, md5Other)
	}

	// SaveAtomic roundtrip
	p := filepath.Join(t.TempDir(), "agent.yaml")
	agentCfg := &Config{
		Mode:       "agent",
		MasterURL:  "http://127.0.0.1:18080/api/v1/report",
		AgentToken: "secret",
		LocalCollector: LocalCollect{
			Enabled:     true,
			NodeID:      "vps-jp",
			IntervalSec: 15,
			WatchPorts:  []int{443, 8443},
		},
	}
	if err := SaveAtomic(p, agentCfg); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.LocalCollector.IntervalSec != 15 || len(loaded.LocalCollector.WatchPorts) != 2 {
		t.Fatalf("unexpected loaded config: %+v", loaded.LocalCollector)
	}
}
