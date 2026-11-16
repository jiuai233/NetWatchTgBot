package service

import (
	"fmt"
	"math"
	"sort"
	"time"

	"netwatch/internal/config"
	"netwatch/internal/geo"
	"netwatch/internal/model"
	"netwatch/internal/store"
)

type Aggregator struct {
	cfg   *config.Config
	store *store.Store
}

func NewAggregator(cfg *config.Config, st *store.Store) *Aggregator {
	return &Aggregator{cfg: cfg, store: st}
}

func (a *Aggregator) SyncConfigNodes() error {
	for _, nd := range a.cfg.Nodes {
		if err := a.store.UpsertNode(nd, true); err != nil {
			return err
		}
	}
	return nil
}

func (a *Aggregator) ActiveNodes() []model.NodeDef {
	list, err := a.store.ListNodes()
	if err == nil && len(list) > 0 {
		return list
	}
	return a.cfg.Nodes
}

func (a *Aggregator) BuildOverview(period string) (model.OverviewView, error) {
	now := time.Now()
	periodLabel := "本月"
	if period == "today" {
		periodLabel = "今天"
	} else if period == "week" {
		periodLabel = "本周"
	}

	daily30, err := a.store.GetDailyTraffic("", 30)
	if err != nil {
		return model.OverviewView{}, err
	}
	samples, err := a.store.GetResourceSamples("", now.Add(-1*time.Hour))
	if err != nil {
		return model.OverviewView{}, err
	}

	allNodes := a.ActiveNodes()
	var totalQuota, totalUsed, cycleRx, cycleTx, totalCycleWithBase int64
	var curBps float64
	nodes := make([]model.NodeSummary, 0, len(allNodes))
	nodeNames := make(map[string]string, len(allNodes))
	resetDay := 1
	expireDate := "--"

	for idx, nd := range allNodes {
		nodeNames[nd.ID] = nd.Name
		if idx == 0 {
			if nd.ResetDay > 0 {
				resetDay = nd.ResetDay
			}
			if nd.ExpireDate != "" {
				expireDate = nd.ExpireDate
			}
		}
		startDate := periodStart(now, period, nd.ResetDay)
		rx, tx, _ := a.store.GetTrafficSince(nd.ID, startDate)
		cycStart := billingCycleStart(now, nd.ResetDay)
		crx, ctx, _ := a.store.GetTrafficSince(nd.ID, cycStart)
		calBase := a.store.GetCycleCalibration(nd.ID, cycStart)

		periodBytes := rx + tx
		cycleBytes := crx + ctx + calBase
		if period == "month" {
			periodBytes += calBase
		}

		nodeSamples, _ := a.store.GetResourceSamples(nd.ID, now.Add(-15*time.Minute))
		probes, _ := a.store.GetProbes(nd.ID)

		var latest model.ResourceSample
		var online bool
		if len(nodeSamples) > 0 {
			latest = nodeSamples[len(nodeSamples)-1]
			online = now.Sub(latest.Timestamp) < 3*time.Minute
		}

		gdLat, gdLoss := calcGDProbeAvg(probes)
		score, hLabel, hNote := calcHealth(online, latest.CPUPct, latest.MemPct, latest.DiskPct, gdLat, gdLoss)
		quotaBytes := nd.QuotaGB * 1024 * 1024 * 1024

		nodes = append(nodes, model.NodeSummary{
			ID:           nd.ID,
			Name:         nd.Name,
			Host:         nd.Host,
			Port:         nd.Port,
			Protocol:     nd.Protocol,
			Online:       online,
			HealthScore:  score,
			HealthLabel:  hLabel,
			HealthNote:   hNote,
			CPUPct:       latest.CPUPct,
			MemPct:       latest.MemPct,
			DiskPct:      latest.DiskPct,
			GDLatencyMs:  gdLat,
			GDLossPct:    gdLoss,
			RxBps:        latest.RxBps,
			TxBps:        latest.TxBps,
			ActiveConns:  latest.ActiveConns,
			QuotaBytes:   quotaBytes,
			UsedBytes:    periodBytes,
			CycleRxBytes: crx,
			CycleTxBytes: ctx,
			ResetDay:     nd.ResetDay,
			ExpireDate:   nd.ExpireDate,
			UpdatedAt:    latest.Timestamp,
		})

		totalQuota += quotaBytes
		totalUsed += periodBytes
		cycleRx += crx
		cycleTx += ctx
		totalCycleWithBase += cycleBytes
		if online {
			curBps += latest.RxBps + latest.TxBps
		}
	}

	topClients, _ := a.store.GetClients("", 12)
	a.enrichClients(topClients, nodeNames)
	cycPct := billingCycleProgress(now, resetDay)
	usedPct := 0.0
	if totalQuota > 0 {
		usedPct = float64(totalCycleWithBase) / float64(totalQuota) * 100.0
	}
	budgetStatus := fmt.Sprintf("周期过去 %.0f%%，已用 %.1f%%，够用", cycPct, usedPct)
	if cycPct > 5 && usedPct > cycPct*1.15 {
		budgetStatus = fmt.Sprintf("周期过去 %.0f%%，已用 %.1f%%，注意流量偏快", cycPct, usedPct)
	}

	return model.OverviewView{
		Title:         "NetWatch 节点监控",
		PeriodLabel:   periodLabel,
		ResetDay:      resetDay,
		ExpireDate:    expireDate,
		TotalQuota:    totalQuota,
		TotalUsed:     totalUsed,
		CycleTx:       cycleTx,
		CycleRx:       cycleRx,
		CycleProgress: cycPct,
		BudgetStatus:  budgetStatus,
		CurrentBps:    curBps,
		Daily30:       daily30,
		SpeedHistory:  samples,
		Nodes:         nodes,
		TopClients:    topClients,
		GeneratedAt:   now,
	}, nil
}

func (a *Aggregator) BuildNodeDetail(nodeID, window string) (model.NodeDetailView, error) {
	now := time.Now()
	allNodes := a.ActiveNodes()
	var target *model.NodeDef
	for i := range allNodes {
		if allNodes[i].ID == nodeID {
			target = &allNodes[i]
			break
		}
	}
	if target == nil && len(allNodes) > 0 {
		target = &allNodes[0]
	}
	if target == nil {
		return model.NodeDetailView{}, fmt.Errorf("node not found: %s", nodeID)
	}

	dur, wLabel := parseWindow(window)
	hist, err := a.store.GetResourceSamples(target.ID, now.Add(-dur))
	if err != nil {
		return model.NodeDetailView{}, err
	}
	daily7, err := a.store.GetDailyTraffic(target.ID, 7)
	if err != nil {
		return model.NodeDetailView{}, err
	}
	probes, err := a.store.GetProbes(target.ID)
	if err != nil {
		return model.NodeDetailView{}, err
	}
	clients, err := a.store.GetClients(target.ID, 12)
	if err != nil {
		return model.NodeDetailView{}, err
	}
	a.enrichClients(clients, map[string]string{target.ID: target.Name})

	var latest model.ResourceSample
	var online bool
	if len(hist) > 0 {
		latest = hist[len(hist)-1]
		online = now.Sub(latest.Timestamp) < 3*time.Minute
	}
	gdLat, gdLoss := calcGDProbeAvg(probes)
	score, hLabel, hNote := calcHealth(online, latest.CPUPct, latest.MemPct, latest.DiskPct, gdLat, gdLoss)
	cycStart := billingCycleStart(now, target.ResetDay)
	crx, ctx, _ := a.store.GetTrafficSince(target.ID, cycStart)
	calBase := a.store.GetCycleCalibration(target.ID, cycStart)

	summary := model.NodeSummary{
		ID:           target.ID,
		Name:         target.Name,
		Host:         target.Host,
		Port:         target.Port,
		Protocol:     target.Protocol,
		Online:       online,
		HealthScore:  score,
		HealthLabel:  hLabel,
		HealthNote:   hNote,
		CPUPct:       latest.CPUPct,
		MemPct:       latest.MemPct,
		DiskPct:      latest.DiskPct,
		GDLatencyMs:  gdLat,
		GDLossPct:    gdLoss,
		RxBps:        latest.RxBps,
		TxBps:        latest.TxBps,
		ActiveConns:  latest.ActiveConns,
		QuotaBytes:   target.QuotaGB * 1024 * 1024 * 1024,
		UsedBytes:    crx + ctx + calBase,
		CycleRxBytes: crx,
		CycleTxBytes: ctx,
		ResetDay:     target.ResetDay,
		ExpireDate:   target.ExpireDate,
		UpdatedAt:    latest.Timestamp,
	}

	sort.Slice(probes, func(i, j int) bool {
		if probes[i].Tag != probes[j].Tag {
			return probes[i].Tag < probes[j].Tag
		}
		return probes[i].Name < probes[j].Name
	})

	return model.NodeDetailView{
		Node:         summary,
		WindowLabel:  wLabel,
		ResourceHist: hist,
		Daily7:       daily7,
		Probes:       probes,
		ClientIPs:    clients,
		GeneratedAt:  now,
	}, nil
}

func (a *Aggregator) CalibrateNodeMonthGB(nodeID string, targetGB float64) error {
	now := time.Now()
	resetDay := 1
	for _, nd := range a.ActiveNodes() {
		if nd.ID == nodeID {
			if nd.ResetDay > 0 {
				resetDay = nd.ResetDay
			}
			break
		}
	}
	cycStart := billingCycleStart(now, resetDay)
	crx, ctx, err := a.store.GetTrafficSince(nodeID, cycStart)
	if err != nil {
		return err
	}
	targetBytes := int64(targetGB * 1024 * 1024 * 1024)
	base := targetBytes - (crx + ctx)
	return a.store.SetCycleCalibration(nodeID, cycStart, base)
}

func (a *Aggregator) AllClientsEnriched(limit int) ([]model.ClientIPStat, error) {
	list, err := a.store.GetClients("", limit)
	if err != nil {
		return nil, err
	}
	allNodes := a.ActiveNodes()
	nodeNames := make(map[string]string, len(allNodes))
	for _, nd := range allNodes {
		nodeNames[nd.ID] = nd.Name
	}
	a.enrichClients(list, nodeNames)
	return list, nil
}

func (a *Aggregator) enrichClients(list []model.ClientIPStat, nodeNames map[string]string) {
	for i := range list {
		if name, ok := nodeNames[list[i].NodeID]; ok {
			list[i].NodeName = name
		} else {
			list[i].NodeName = list[i].NodeID
		}
		if list[i].Region == "" {
			if reg, ok := a.store.GetGeo(list[i].IP); ok {
				list[i].Region = reg
			} else {
				reg := geo.Lookup(list[i].IP)
				if reg != "" {
					_ = a.store.SaveGeo(list[i].IP, reg)
					list[i].Region = reg
				}
			}
		}
	}
}

func parseWindow(w string) (time.Duration, string) {
	switch w {
	case "6h":
		return 6 * time.Hour, "6 小时"
	case "24h":
		return 24 * time.Hour, "24 小时"
	case "3d":
		return 72 * time.Hour, "3 天"
	case "7d":
		return 7 * 24 * time.Hour, "7 天"
	default:
		return 1 * time.Hour, "1 小时"
	}
}

func periodStart(now time.Time, period string, resetDay int) string {
	switch period {
	case "today":
		return now.Format("2006-01-02")
	case "week":
		wd := int(now.Weekday())
		if wd == 0 {
			wd = 7
		}
		return now.AddDate(0, 0, -(wd - 1)).Format("2006-01-02")
	default:
		return billingCycleStart(now, resetDay)
	}
}

func billingCycleStart(now time.Time, resetDay int) string {
	if resetDay <= 0 || resetDay > 28 {
		resetDay = 1
	}
	y, m, d := now.Date()
	if d >= resetDay {
		return fmt.Sprintf("%04d-%02d-%02d", y, int(m), resetDay)
	}
	prev := time.Date(y, m, 1, 0, 0, 0, 0, now.Location()).AddDate(0, -1, 0)
	return fmt.Sprintf("%04d-%02d-%02d", prev.Year(), int(prev.Month()), resetDay)
}

func billingCycleProgress(now time.Time, resetDay int) float64 {
	startStr := billingCycleStart(now, resetDay)
	start, err := time.ParseInLocation("2006-01-02", startStr, now.Location())
	if err != nil {
		return 50
	}
	end := start.AddDate(0, 1, 0)
	pct := now.Sub(start).Seconds() / end.Sub(start).Seconds() * 100.0
	return math.Max(0, math.Min(100, pct))
}

func calcGDProbeAvg(probes []model.ProbeEntry) (avgLat, avgLoss float64) {
	var sumLat, sumLoss float64
	var cnt, okCnt int
	for _, p := range probes {
		if p.Tag == "gd" {
			cnt++
			sumLoss += p.LossPct
			if p.LatencyMs > 0 {
				sumLat += p.LatencyMs
				okCnt++
			}
		}
	}
	if cnt == 0 {
		return 0, 0
	}
	if okCnt > 0 {
		avgLat = sumLat / float64(okCnt)
	}
	return avgLat, sumLoss / float64(cnt)
}

func calcHealth(online bool, cpu, mem, disk, gdLat, gdLoss float64) (int, string, string) {
	if !online {
		return 0, "离线", "节点心跳超时"
	}
	score := 100.0
	note := "运行状态良好"
	if gdLat > 200 {
		score -= 10
		note = "网络延迟偏高"
	} else if gdLat > 120 {
		score -= 6
		note = "网络延迟偏高"
	}
	if gdLoss > 5 {
		score -= math.Min(30, gdLoss*1.5)
		note = "广东探测存在丢包"
	}
	if cpu > 80 {
		score -= (cpu - 80) * 0.5
		note = "CPU 负载偏高"
	}
	if mem > 85 {
		score -= (mem - 85) * 0.6
		note = "内存占用偏高"
	}
	if disk > 85 {
		score -= (disk - 85) * 0.6
		note = "磁盘空间偏紧"
	}
	s := int(math.Round(math.Max(0, math.Min(100, score))))
	label := "卓越"
	if s < 60 {
		label = "异常"
	} else if s < 85 {
		label = "良好"
	}
	return s, label, note
}
