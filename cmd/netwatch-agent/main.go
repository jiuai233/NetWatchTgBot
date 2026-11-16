package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"log"
	"net/http"
	"os/signal"
	"syscall"
	"time"

	"netwatch/internal/collector"
	"netwatch/internal/config"
	"netwatch/internal/model"
)

func main() {
	cfgPath := flag.String("config", "agent.yaml", "path to agent config yaml")
	flag.Parse()

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
