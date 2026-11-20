package model

import "time"

type DailyTraffic struct {
	Date    string // YYYY-MM-DD
	RxBytes int64
	TxBytes int64
}

type ResourceSample struct {
	Timestamp    time.Time
	CPUPct       float64
	MemPct       float64
	DiskPct      float64
	RxBps        float64 // bytes/sec
	TxBps        float64 // bytes/sec
	AvgLatencyMs float64
	ActiveConns  int
}

type ProbeEntry struct {
	Name      string
	Tag       string  // e.g. "GD-CMCC", "GD-CT", "GD-CU", "intl"
	LatencyMs float64 // <= 0 means unreachable/timeout
	LossPct   float64
	Status    string // "ok", "slow", "down"
	UpdatedAt time.Time
}

type ClientIPStat struct {
	NodeID      string
	NodeName    string
	IP          string
	Region      string // e.g. "广东广州 · 电信"
	ActiveConns int
	RxBps       float64
	TxBps       float64
	TotalRx     int64 // persisted cumulative bytes
	TotalTx     int64 // persisted cumulative bytes
	LastSeen    time.Time
}

type NodeDef struct {
	ID           string `yaml:"id" json:"id"`
	Name         string `yaml:"name" json:"name"`
	Host         string `yaml:"host" json:"host"`
	Port         int    `yaml:"port" json:"port"`
	ProbePort    int    `yaml:"probe_port,omitempty" json:"probe_port,omitempty"` // optional TCP port for Globalping probe (e.g. 23333 for Hysteria2 node)
	Protocol     string `yaml:"protocol" json:"protocol"`
	QuotaGB      int64  `yaml:"quota_gb" json:"quota_gb"`
	ResetDay     int    `yaml:"reset_day" json:"reset_day"`
	ExpireDate   string `yaml:"expire_date" json:"expire_date"`
	AutoRenew    bool   `yaml:"auto_renew,omitempty" json:"auto_renew,omitempty"`
	BillingCycle string `yaml:"billing_cycle,omitempty" json:"billing_cycle,omitempty"` // "monthly" | "yearly"
}

type NodeSummary struct {
	ID            string
	Name          string
	Host          string
	Port          int
	Protocol      string // e.g. "Hysteria2", "VLESS-Reality"
	Online        bool
	ProxyState    string // "ok" | "slow" | "down" | "unknown"
	HealthScore   int
	HealthLabel   string
	HealthNote    string
	CPUPct        float64
	MemPct        float64
	DiskPct       float64
	GDLatencyMs   float64 // avg Guangdong latency
	GDLossPct     float64
	GDProbeCount  int
	GDOkCount     int
	RxBps         float64
	TxBps         float64
	ActiveConns   int
	QuotaBytes    int64
	UsedBytes     int64
	CycleRxBytes  int64
	CycleTxBytes  int64
	ResetDay      int
	ExpireDate    string
	UpdatedAt     time.Time
}

type OverviewView struct {
	Title         string
	PeriodLabel   string // "今天", "本周", "本月"
	ResetDay      int
	ExpireDate    string
	TotalQuota    int64
	TotalUsed     int64
	CycleTx       int64
	CycleRx       int64
	CycleProgress float64 // 0..100% of billing cycle elapsed
	BudgetStatus  string  // e.g. "周期过去 49%，已用 7%，够用"
	CurrentBps    float64
	Daily30       []DailyTraffic
	SpeedHistory  []ResourceSample
	Nodes         []NodeSummary
	TopClients    []ClientIPStat
	GeneratedAt   time.Time
}

type NodeDetailView struct {
	Node          NodeSummary
	WindowLabel   string // "1 小时", "6 小时", "24 小时", "3 天", "7 天"
	ResourceHist  []ResourceSample
	Daily7        []DailyTraffic
	Probes        []ProbeEntry
	ClientIPs     []ClientIPStat
	GeneratedAt   time.Time
}

type AgentReport struct {
	NodeID      string         `json:"node_id"`
	NodeMeta    *NodeDef       `json:"node_meta,omitempty"`
	Timestamp   time.Time      `json:"timestamp"`
	CPUPct      float64        `json:"cpu_pct"`
	MemPct      float64        `json:"mem_pct"`
	DiskPct     float64        `json:"disk_pct"`
	NetRxTotal  int64          `json:"net_rx_total"`  // raw OS counter (monotonic until reboot)
	NetTxTotal  int64          `json:"net_tx_total"`  // raw OS counter (monotonic until reboot)
	RxBps       float64        `json:"rx_bps"`
	TxBps       float64        `json:"tx_bps"`
	ActiveConns int            `json:"active_conns"`
	Probes      []ProbeEntry   `json:"probes,omitempty"`
	Clients     []ClientSample `json:"clients,omitempty"`
}

type TargetDef struct {
	Name string `yaml:"name" json:"name"`
	Tag  string `yaml:"tag" json:"tag"`
	Addr string `yaml:"addr" json:"addr"`
}

type AgentSyncConfig struct {
	IntervalSec   int         `yaml:"interval_sec,omitempty" json:"interval_sec,omitempty"`
	WatchPorts    []int       `yaml:"watch_ports,omitempty" json:"watch_ports,omitempty"`
	LocalTargets  []TargetDef `yaml:"local_targets,omitempty" json:"local_targets,omitempty"`
	AutoUpdate    *bool       `yaml:"auto_update,omitempty" json:"auto_update,omitempty"`
	TargetVersion string      `yaml:"target_version,omitempty" json:"target_version,omitempty"`
	UpdateRepo    string      `yaml:"update_repo,omitempty" json:"update_repo,omitempty"`
	UpdateProxy   *string     `yaml:"update_proxy,omitempty" json:"update_proxy,omitempty"`
}

type ReportResponse struct {
	OK          bool             `json:"ok"`
	ConfigMD5   string           `json:"config_md5,omitempty"`
	AgentConfig *AgentSyncConfig `json:"agent_config,omitempty"`
}

type ClientSample struct {
	IP          string  `json:"ip"`
	ActiveConns int     `json:"active_conns"`
	RxBps       float64 `json:"rx_bps"`
	TxBps       float64 `json:"tx_bps"`
	SessionRx   int64   `json:"session_rx"` // cumulative in current process/session; delta persisted
	SessionTx   int64   `json:"session_tx"`
}
