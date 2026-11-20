package config

import (
	"os"

	"netwatch/internal/model"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Mode            string                          `yaml:"mode"` // "master" or "agent"
	ListenAddr      string                          `yaml:"listen_addr"`
	AgentToken      string                          `yaml:"agent_token"`
	DBPath          string                          `yaml:"db_path"`
	MasterURL       string                          `yaml:"master_url"`       // used in agent mode
	GlobalpingToken string                          `yaml:"globalping_token"` // optional
	Telegram        TGConfig                        `yaml:"telegram"`
	LocalCollector  LocalCollect                    `yaml:"local_collector"`
	Nodes           []model.NodeDef                 `yaml:"nodes"`
	AgentDefaults   *model.AgentSyncConfig          `yaml:"agent_defaults,omitempty"`
	AgentOverrides  map[string]model.AgentSyncConfig `yaml:"agent_overrides,omitempty"`
}

type TGConfig struct {
	BotToken       string  `yaml:"bot_token"`
	AllowedUserIDs []int64 `yaml:"allowed_user_ids"`
}

type LocalCollect struct {
	Enabled      bool              `yaml:"enabled"`
	NodeID       string            `yaml:"node_id"`
	Interface    string            `yaml:"interface"`
	WatchPorts   []int             `yaml:"watch_ports"`
	ClashAPIURL  string            `yaml:"clash_api_url"`
	ClashSecret  string            `yaml:"clash_secret"`
	IntervalSec  int               `yaml:"interval_sec"`
	LocalTargets []model.TargetDef `yaml:"local_targets,omitempty"`
	AutoUpdate   bool              `yaml:"auto_update,omitempty"`
	UpdateRepo   string            `yaml:"update_repo,omitempty"`
	UpdateProxy  string            `yaml:"update_proxy,omitempty"`
	Register     *model.NodeDef    `yaml:"register,omitempty"` // sent by agent for auto-registration
}

func Load(path string) (*Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var c Config
	if err := yaml.Unmarshal(b, &c); err != nil {
		return nil, err
	}
	if c.DBPath == "" {
		c.DBPath = "netwatch.db"
	}
	if c.ListenAddr == "" {
		c.ListenAddr = ":18080"
	}
	if c.LocalCollector.IntervalSec <= 0 {
		c.LocalCollector.IntervalSec = 30
	}
	return &c, nil
}
