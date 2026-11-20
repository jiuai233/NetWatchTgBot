package config

import (
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"

	"netwatch/internal/model"

	"gopkg.in/yaml.v3"
)

func DefaultLocalTargets() []model.TargetDef {
	return []model.TargetDef{
		{Name: "Cloudflare DNS · intl", Tag: "intl", Addr: "1.1.1.1:443"},
		{Name: "Google DNS · intl", Tag: "intl", Addr: "8.8.8.8:443"},
	}
}

// ResolveAgentSyncConfig merges global agent_defaults with per-node agent_overrides.
// Returns (nil, "") when no managed override is configured.
func ResolveAgentSyncConfig(cfg *Config, nodeID string) (*model.AgentSyncConfig, string) {
	if cfg == nil {
		return nil, ""
	}
	var merged model.AgentSyncConfig
	hasAny := false

	apply := func(src *model.AgentSyncConfig) {
		if src == nil {
			return
		}
		if src.IntervalSec > 0 {
			merged.IntervalSec = src.IntervalSec
			hasAny = true
		}
		if src.WatchPorts != nil {
			cp := make([]int, len(src.WatchPorts))
			copy(cp, src.WatchPorts)
			merged.WatchPorts = cp
			hasAny = true
		}
		if src.LocalTargets != nil {
			cp := make([]model.TargetDef, len(src.LocalTargets))
			copy(cp, src.LocalTargets)
			merged.LocalTargets = cp
			hasAny = true
		}
		if src.AutoUpdate != nil {
			v := *src.AutoUpdate
			merged.AutoUpdate = &v
			hasAny = true
		}
		if src.TargetVersion != "" {
			merged.TargetVersion = src.TargetVersion
			hasAny = true
		}
		if src.UpdateRepo != "" {
			merged.UpdateRepo = src.UpdateRepo
			hasAny = true
		}
		if src.UpdateProxy != nil {
			v := *src.UpdateProxy
			merged.UpdateProxy = &v
			hasAny = true
		}
	}

	apply(cfg.AgentDefaults)
	if nodeID != "" && cfg.AgentOverrides != nil {
		if ov, ok := cfg.AgentOverrides[nodeID]; ok {
			apply(&ov)
		}
	}
	if !hasAny {
		return nil, ""
	}
	raw, err := json.Marshal(merged)
	if err != nil {
		return &merged, ""
	}
	sum := md5.Sum(raw)
	return &merged, hex.EncodeToString(sum[:])
}

// SaveAtomic serializes cfg to YAML via temp file + rename to prevent partial writes.
func SaveAtomic(path string, cfg *Config) error {
	raw, err := yaml.Marshal(cfg)
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
