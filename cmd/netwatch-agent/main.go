package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"netwatch/internal/agent"
	"netwatch/internal/config"
)

var version = "dev"

func main() {
	cfgPath := flag.String("config", "agent.yaml", "path to agent config yaml")
	showVer := flag.Bool("version", false, "print agent version and exit")
	selfUpdate := flag.Bool("self-update", false, "check latest GitHub release and update immediately")
	updateRepo := flag.String("repo", "", "override GitHub repo (owner/name) for -self-update")
	updateProxy := flag.String("proxy", "", "override download proxy prefix for -self-update")
	flag.Parse()

	if *showVer {
		fmt.Println(version)
		return
	}

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		log.Fatal(err)
	}

	if *selfUpdate {
		repo := *updateRepo
		if repo == "" {
			repo = cfg.LocalCollector.UpdateRepo
		}
		proxy := *updateProxy
		if proxy == "" {
			proxy = cfg.LocalCollector.UpdateProxy
		}
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cancel()
		if err := agent.NewUpdater(*cfgPath, version).CheckAndSchedule(ctx, repo, proxy, true); err != nil {
			log.Printf("self-update failed: %v", err)
			os.Exit(1)
		}
		log.Println("self-update check finished")
		return
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	agent.NewRunner(cfg, *cfgPath, version).Run(ctx)
}
