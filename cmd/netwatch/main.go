package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"math"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"netwatch/internal/bot"
	"netwatch/internal/collector"
	"netwatch/internal/config"
	"netwatch/internal/model"
	"netwatch/internal/probe"
	"netwatch/internal/render"
	"netwatch/internal/service"
	"netwatch/internal/store"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Println("Usage: netwatch <preview|master|agent> [flags]")
		os.Exit(1)
	}
	sub := os.Args[1]
	switch sub {
	case "preview":
		runPreview(os.Args[2:])
	case "master":
		runMaster(os.Args[2:])
	case "agent":
		runAgent(os.Args[2:])
	case "calibrate":
		runCalibrate(os.Args[2:])
	default:
		fmt.Printf("unknown subcommand: %s\n", sub)
		os.Exit(1)
	}
}

func runPreview(args []string) {
	fs := flag.NewFlagSet("preview", flag.ExitOnError)
	outDir := fs.String("out", "preview_out", "output directory for preview PNGs and text")
	liveGD := fs.Bool("live-gd", false, "run live Globalping Guangzhou/Shenzhen TCP probe")
	liveTarget := fs.String("target", "1.1.1.1", "target host for --live-gd")
	livePort := fs.Int("port", 443, "target TCP port for --live-gd")
	_ = fs.Parse(args)

	if err := os.MkdirAll(*outDir, 0o755); err != nil {
		log.Fatal(err)
	}

	now := time.Now()
	daily30 := make([]model.DailyTraffic, 30)
	for i := 0; i < 30; i++ {
		dt := now.AddDate(0, 0, -(29 - i)).Format("2006-01-02")
		rx := int64((1.2 + 2.4*math.Abs(math.Sin(float64(i)*0.47))) * 1024 * 1024 * 1024)
		tx := int64((0.8 + 1.5*math.Abs(math.Cos(float64(i)*0.33))) * 1024 * 1024 * 1024)
		daily30[i] = model.DailyTraffic{Date: dt, RxBytes: rx, TxBytes: tx}
	}

	samples := make([]model.ResourceSample, 30)
	for i := 0; i < 30; i++ {
		ts := now.Add(time.Duration(i-29) * 2 * time.Minute)
		samples[i] = model.ResourceSample{
			Timestamp:   ts,
			CPUPct:      3 + 4*math.Abs(math.Sin(float64(i)*0.3)),
			MemPct:      12 + 2*math.Sin(float64(i)*0.2),
			DiskPct:     16,
			RxBps:       (120 + 280*math.Abs(math.Sin(float64(i)*0.4))) * 1024,
			TxBps:       (45 + 110*math.Abs(math.Cos(float64(i)*0.35))) * 1024,
			ActiveConns: 18 + i%9,
		}
	}

	probes216 := []model.ProbeEntry{
		{Name: "广东广州 · 移动", Tag: "gd", LatencyMs: 44.5, LossPct: 0, Status: "ok", UpdatedAt: now},
		{Name: "广东深圳 · 移动", Tag: "gd", LatencyMs: 81.5, LossPct: 0, Status: "ok", UpdatedAt: now},
		{Name: "广东广州 · 腾讯云", Tag: "gd", LatencyMs: 90.9, LossPct: 0, Status: "ok", UpdatedAt: now},
		{Name: "广东深圳 · 阿里云", Tag: "gd", LatencyMs: 92.9, LossPct: 0, Status: "ok", UpdatedAt: now},
		{Name: "Cloudflare DNS · intl", Tag: "intl", LatencyMs: 3.2, LossPct: 0, Status: "ok", UpdatedAt: now},
		{Name: "Google DNS · intl", Tag: "intl", LatencyMs: 4.1, LossPct: 0, Status: "ok", UpdatedAt: now},
	}

	if *liveGD {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		gdLive, err := probe.MeasureGuangdong(ctx, *liveTarget, *livePort, 0, "VLESS", "")
		cancel()
		if err != nil {
			log.Printf("live Globalping failed (falling back to sample): %v", err)
		} else if len(gdLive) > 0 {
			probes216 = append(gdLive,
				model.ProbeEntry{Name: "Cloudflare DNS · intl", Tag: "intl", LatencyMs: 3.2, LossPct: 0, Status: "ok", UpdatedAt: now},
				model.ProbeEntry{Name: "Google DNS · intl", Tag: "intl", LatencyMs: 4.1, LossPct: 0, Status: "ok", UpdatedAt: now},
			)
		}
	}

	var gdAvg float64
	var gdCnt int
	for _, p := range probes216 {
		if p.Tag == "gd" && p.LatencyMs > 0 {
			gdAvg += p.LatencyMs
			gdCnt++
		}
	}
	if gdCnt > 0 {
		gdAvg /= float64(gdCnt)
	}

	gb := func(v float64) int64 { return int64(v * 1024 * 1024 * 1024) }
	clients := []model.ClientIPStat{
		{NodeID: "vps-us", NodeName: "美国节点", IP: "198.51.100.11", Region: "广东广州 · 电信", ActiveConns: 21, RxBps: 420 * 1024, TxBps: 68 * 1024, TotalRx: gb(14.2), TotalTx: gb(2.1), LastSeen: now},
		{NodeID: "vps-sg", NodeName: "新加坡节点", IP: "198.51.100.22", Region: "广东深圳 · 移动", ActiveConns: 6, RxBps: 84 * 1024, TxBps: 19 * 1024, TotalRx: gb(4.8), TotalTx: gb(0.9), LastSeen: now},
		{NodeID: "vps-us", NodeName: "美国节点", IP: "198.51.100.33", Region: "广东佛山 · 联通", ActiveConns: 0, RxBps: 0, TxBps: 0, TotalRx: gb(2.3), TotalTx: gb(0.4), LastSeen: now.Add(-2 * time.Hour)},
		{NodeID: "vps-sg", NodeName: "新加坡节点", IP: "198.51.100.44", Region: "广东广州 · 移动", ActiveConns: 2, RxBps: 15 * 1024, TxBps: 4 * 1024, TotalRx: 890 * 1024 * 1024, TotalTx: 140 * 1024 * 1024, LastSeen: now},
	}

	ov := model.OverviewView{
		Title:         "NetWatch 节点监控",
		PeriodLabel:   "本月",
		ResetDay:      2,
		ExpireDate:    "2026-12-02",
		TotalQuota:    gb(500),
		TotalUsed:     gb(25.43),
		CycleTx:       gb(6.21),
		CycleRx:       gb(19.22),
		CycleProgress: 49,
		BudgetStatus:  "周期过去 49%，已用 5.1%，够用",
		CurrentBps:    610 * 1024,
		Daily30:       daily30,
		SpeedHistory:  samples,
		Nodes: []model.NodeSummary{
			{
				ID: "vps-us", Name: "美国节点", Host: "203.0.113.10", Port: 51102, Protocol: "Hysteria2",
				Online: true, HealthScore: 93, HealthLabel: "卓越", HealthNote: "延迟稳定，负载极低",
				CPUPct: 1.2, MemPct: 5.0, DiskPct: 16.0, GDLatencyMs: 158,
				RxBps: 260 * 1024, TxBps: 52 * 1024, ActiveConns: 14,
				QuotaBytes: gb(700), UsedBytes: gb(320.4), ResetDay: 4, ExpireDate: "2026-12-04",
			},
			{
				ID: "vps-sg", Name: "新加坡节点", Host: "203.0.113.20", Port: 57423, Protocol: "VLESS",
				Online: true, HealthScore: 95, HealthLabel: "卓越", HealthNote: "广东三网延迟优秀",
				CPUPct: 3.5, MemPct: 5.2, DiskPct: 7.5, GDLatencyMs: gdAvg,
				RxBps: 240 * 1024, TxBps: 58 * 1024, ActiveConns: 15,
				QuotaBytes: gb(800), UsedBytes: gb(12.58), ResetDay: 26, ExpireDate: "2026-11-26",
			},
			{
				ID: "vps-us2", Name: "备用节点", Host: "203.0.113.30", Port: 20004, Protocol: "VLESS",
				Online: false, HealthScore: 0, HealthLabel: "离线", HealthNote: "20004 端口超时",
				QuotaBytes: gb(100), UsedBytes: 19 * 1024 * 1024, ResetDay: 1, ExpireDate: "2026-12-01",
			},
		},
		TopClients:  clients,
		GeneratedAt: now,
	}

	nd := model.NodeDetailView{
		Node: model.NodeSummary{
			ID: "vps-sg", Name: "新加坡节点", Host: "203.0.113.20", Port: 57423, Protocol: "VLESS",
			Online: true, HealthScore: 95, HealthLabel: "卓越", HealthNote: "广东三网延迟优秀，容器资源充裕",
			CPUPct: 4, MemPct: 5, DiskPct: 8, GDLatencyMs: gdAvg,
			RxBps: 240 * 1024, TxBps: 58 * 1024, ActiveConns: 15,
		},
		WindowLabel:  "1 小时",
		ResourceHist: samples,
		Daily7:       daily30[len(daily30)-7:],
		Probes:       probes216,
		ClientIPs:    clients,
		GeneratedAt:  now,
	}

	ovPNG := filepath.Join(*outDir, "preview_overview.png")
	ndPNG := filepath.Join(*outDir, "preview_node_detail.png")
	if err := render.RenderOverviewPNG(ov, ovPNG); err != nil {
		log.Fatal(err)
	}
	if err := render.RenderNodeDetailPNG(nd, ndPNG); err != nil {
		log.Fatal(err)
	}

	ovTxt := render.RenderOverviewText(ov)
	ndTxt := render.RenderNodeDetailText(nd)
	_ = os.WriteFile(filepath.Join(*outDir, "preview_overview.txt"), []byte(ovTxt), 0o644)
	_ = os.WriteFile(filepath.Join(*outDir, "preview_node_detail.txt"), []byte(ndTxt), 0o644)

	fmt.Println("Generated:")
	fmt.Println(" ", ovPNG)
	fmt.Println(" ", ndPNG)
	fmt.Println("\n--- Text Preview (Overview) ---")
	fmt.Println(ovTxt)
}

func runMaster(args []string) {
	fs := flag.NewFlagSet("master", flag.ExitOnError)
	cfgPath := fs.String("config", "config.yaml", "path to config.yaml")
	_ = fs.Parse(args)

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		log.Fatal(err)
	}
	st, err := store.Open(cfg.DBPath)
	if err != nil {
		log.Fatal(err)
	}
	defer st.Close()

	agg := service.NewAggregator(cfg, st)
	if err := agg.SyncConfigNodes(); err != nil {
		log.Fatal(err)
	}
	tgBot := bot.New(cfg, agg, st)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	knownNodes := make(map[string]bool)
	for _, nd := range agg.ActiveNodes() {
		knownNodes[nd.ID] = true
	}

	// HTTP endpoint to receive Agent reports (auto-registers new nodes)
	mux := http.NewServeMux()
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
		if err := st.IngestReport(rep); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	})

	srv := &http.Server{Addr: cfg.ListenAddr, Handler: mux}
	go func() {
		log.Printf("master listening on %s", cfg.ListenAddr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("http server err: %v", err)
		}
	}()

	// Optional local collector on master node itself
	if cfg.LocalCollector.Enabled {
		go runLocalCollector(ctx, cfg, st)
	}

	// Periodic Globalping Guangdong probe every 30 minutes
	go func() {
		runGD := func() {
			for _, nd := range agg.ActiveNodes() {
				if nd.Host == "" {
					continue
				}
				pCtx, cancel := context.WithTimeout(ctx, 25*time.Second)
				res, err := probe.MeasureGuangdong(pCtx, nd.Host, nd.Port, nd.ProbePort, nd.Protocol, cfg.GlobalpingToken)
				cancel()
				if err == nil && len(res) > 0 {
					_ = st.SaveProbes(nd.ID, res)
				}
			}
		}
		runGD()
		tk := time.NewTicker(30 * time.Minute)
		defer tk.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tk.C:
				runGD()
			}
		}
	}()

	go tgBot.Run(ctx)

	<-ctx.Done()
	_ = srv.Shutdown(context.Background())
}

func runLocalCollector(ctx context.Context, cfg *config.Config, st *store.Store) {
	col := collector.New(collector.Options{
		NodeID:      cfg.LocalCollector.NodeID,
		Interface:   cfg.LocalCollector.Interface,
		WatchPorts:  cfg.LocalCollector.WatchPorts,
		ClashAPIURL: cfg.LocalCollector.ClashAPIURL,
		ClashSecret: cfg.LocalCollector.ClashSecret,
		LocalTargets: []collector.Target{
			{Name: "Cloudflare DNS · intl", Tag: "intl", Addr: "1.1.1.1:443"},
			{Name: "Google DNS · intl", Tag: "intl", Addr: "8.8.8.8:443"},
		},
	})
	// Warm up baseline CPU/net
	_ = col.Collect(false)
	time.Sleep(1 * time.Second)
	_ = st.IngestReport(col.Collect(true))

	tk := time.NewTicker(time.Duration(cfg.LocalCollector.IntervalSec) * time.Second)
	defer tk.Stop()
	tickCount := 0
	for {
		select {
		case <-ctx.Done():
			return
		case <-tk.C:
			tickCount++
			rep := col.Collect(tickCount%4 == 0)
			if err := st.IngestReport(rep); err != nil {
				log.Printf("local ingest err: %v", err)
			}
		}
	}
}

func runAgent(args []string) {
	fs := flag.NewFlagSet("agent", flag.ExitOnError)
	cfgPath := fs.String("config", "agent.yaml", "path to agent config yaml")
	_ = fs.Parse(args)

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		log.Fatal(err)
	}
	col := collector.New(collector.Options{
		NodeID:      cfg.LocalCollector.NodeID,
		Interface:   cfg.LocalCollector.Interface,
		WatchPorts:  cfg.LocalCollector.WatchPorts,
		ClashAPIURL: cfg.LocalCollector.ClashAPIURL,
		ClashSecret: cfg.LocalCollector.ClashSecret,
		LocalTargets: []collector.Target{
			{Name: "Cloudflare DNS · intl", Tag: "intl", Addr: "1.1.1.1:443"},
			{Name: "Google DNS · intl", Tag: "intl", Addr: "8.8.8.8:443"},
		},
	})

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	client := &http.Client{Timeout: 10 * time.Second}
	send := func(rep model.AgentReport) {
		rep.NodeMeta = cfg.LocalCollector.Register
		raw, _ := json.Marshal(rep)
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, cfg.MasterURL, bytes.NewReader(raw))
		if err != nil {
			return
		}
		req.Header.Set("Content-Type", "application/json")
		if cfg.AgentToken != "" {
			req.Header.Set("X-Agent-Token", cfg.AgentToken)
		}
		resp, err := client.Do(req)
		if err != nil {
			log.Printf("agent report err: %v", err)
			return
		}
		resp.Body.Close()
	}

	_ = col.Collect(false)
	time.Sleep(1 * time.Second)
	send(col.Collect(true))

	tk := time.NewTicker(time.Duration(cfg.LocalCollector.IntervalSec) * time.Second)
	defer tk.Stop()
	tickCount := 0
	for {
		select {
		case <-ctx.Done():
			return
		case <-tk.C:
			tickCount++
			send(col.Collect(tickCount%4 == 0))
		}
	}
}

func runCalibrate(args []string) {
	fs := flag.NewFlagSet("calibrate", flag.ExitOnError)
	cfgPath := fs.String("config", "config.yaml", "path to config.yaml")
	nodeID := fs.String("node", "", "node id")
	gbStr := fs.String("gb", "", "used GB in current billing cycle")
	_ = fs.Parse(args)

	if *nodeID == "" || *gbStr == "" {
		log.Fatal("usage: netwatch calibrate -config config.yaml -node vps-154 -gb 320.4")
	}
	gbVal, err := strconv.ParseFloat(*gbStr, 64)
	if err != nil {
		log.Fatal(err)
	}
	cfg, err := config.Load(*cfgPath)
	if err != nil {
		log.Fatal(err)
	}
	st, err := store.Open(cfg.DBPath)
	if err != nil {
		log.Fatal(err)
	}
	defer st.Close()
	agg := service.NewAggregator(cfg, st)
	if err := agg.CalibrateNodeMonthGB(*nodeID, gbVal); err != nil {
		log.Fatal(err)
	}
	fmt.Printf("calibrated %s to %.2f GB\n", *nodeID, gbVal)
}

func clientIPFromReq(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		parts := strings.Split(xff, ",")
		if len(parts) > 0 {
			return strings.TrimSpace(parts[0])
		}
	}
	if xrip := strings.TrimSpace(r.Header.Get("X-Real-IP")); xrip != "" {
		return xrip
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err == nil {
		return host
	}
	return r.RemoteAddr
}
