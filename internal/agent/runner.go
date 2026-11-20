package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"sync"
	"time"

	"netwatch/internal/collector"
	"netwatch/internal/config"
	"netwatch/internal/model"
)

const (
	defaultMaxPendingReports = 120
	maxFlushPerTick          = 20
	probeSampleInterval      = 20 * time.Second
)

type Runner struct {
	mu          sync.Mutex
	cfg         *config.Config
	cfgPath     string
	version     string
	cfgMD5      string
	col         *collector.Collector
	client      *http.Client
	updater     *Updater
	queue       []model.AgentReport
	maxQueue    int
	failStreak  int
	nextRetryAt time.Time
	intervalCh  chan time.Duration
	lastProbeAt time.Time
}

func NewRunner(cfg *config.Config, cfgPath, version string) *Runner {
	if version == "" {
		version = "dev"
	}
	targets := cfg.LocalCollector.LocalTargets
	if len(targets) == 0 {
		targets = config.DefaultLocalTargets()
	}
	col := collector.New(collector.Options{
		NodeID:       cfg.LocalCollector.NodeID,
		Interface:    cfg.LocalCollector.Interface,
		WatchPorts:   cfg.LocalCollector.WatchPorts,
		ClashAPIURL:  cfg.LocalCollector.ClashAPIURL,
		ClashSecret:  cfg.LocalCollector.ClashSecret,
		LocalTargets: targets,
	})
	return &Runner{
		cfg:        cfg,
		cfgPath:    cfgPath,
		version:    version,
		col:        col,
		client:     &http.Client{Timeout: 10 * time.Second},
		updater:    NewUpdater(cfgPath, version),
		maxQueue:   defaultMaxPendingReports,
		intervalCh: make(chan time.Duration, 1),
	}
}

func (r *Runner) Run(ctx context.Context) {
	go r.col.StartProbeLoop(ctx, probeSampleInterval)
	go r.autoUpdateLoop(ctx)

	// Warm up CPU/net deltas before sending initial report
	_ = r.col.Collect(false)
	select {
	case <-ctx.Done():
		return
	case <-time.After(1 * time.Second):
	}

	r.enqueue(r.col.Collect(true))
	r.flush(ctx)

	interval := r.currentInterval()
	tk := time.NewTicker(interval)
	defer tk.Stop()
	tickCount := 0

	for {
		select {
		case <-ctx.Done():
			return
		case nextInt := <-r.intervalCh:
			if nextInt != interval && nextInt >= 5*time.Second {
				interval = nextInt
				tk.Reset(interval)
			}
		case <-tk.C:
			tickCount++
			withProbes := tickCount%2 == 0 || time.Since(r.lastProbeAt) >= 60*time.Second
			rep := r.col.Collect(withProbes)
			if withProbes {
				r.lastProbeAt = time.Now()
			}
			r.enqueue(rep)
			r.flush(ctx)
		}
	}
}

func (r *Runner) currentInterval() time.Duration {
	r.mu.Lock()
	defer r.mu.Unlock()
	sec := r.cfg.LocalCollector.IntervalSec
	if sec < 5 {
		sec = 30
	}
	return time.Duration(sec) * time.Second
}

func (r *Runner) enqueue(rep model.AgentReport) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.cfg.LocalCollector.Register != nil {
		cp := *r.cfg.LocalCollector.Register
		rep.NodeMeta = &cp
	}
	r.queue = append(r.queue, rep)
	if len(r.queue) > r.maxQueue {
		drop := len(r.queue) - r.maxQueue
		r.queue = append([]model.AgentReport(nil), r.queue[drop:]...)
		log.Printf("agent queue full, dropped %d oldest sample(s)", drop)
	}
}

func (r *Runner) flush(ctx context.Context) {
	r.mu.Lock()
	if !r.nextRetryAt.IsZero() && time.Now().Before(r.nextRetryAt) {
		r.mu.Unlock()
		return
	}
	r.mu.Unlock()

	sent := 0
	for sent < maxFlushPerTick {
		r.mu.Lock()
		if len(r.queue) == 0 {
			r.mu.Unlock()
			return
		}
		rep := r.queue[0]
		masterURL := r.cfg.MasterURL
		token := r.cfg.AgentToken
		cfgMD5 := r.cfgMD5
		r.mu.Unlock()

		respData, status, err := r.postReport(ctx, masterURL, token, cfgMD5, rep)
		if err != nil {
			r.recordFailure(false)
			log.Printf("agent report err (queued=%d): %v", r.queueLen(), err)
			return
		}
		if status == http.StatusUnauthorized || status == http.StatusForbidden {
			// Auth error or node retired: drop backlog to avoid infinite flood and back off hard
			r.mu.Lock()
			if len(r.queue) > 1 {
				r.queue = r.queue[len(r.queue)-1:]
			}
			r.mu.Unlock()
			r.recordFailure(true)
			log.Printf("agent report rejected http %d, backing off", status)
			return
		}
		if status < 200 || status >= 300 {
			r.recordFailure(false)
			log.Printf("agent report http %d (queued=%d)", status, r.queueLen())
			return
		}

		// Success: dequeue and reset backoff
		r.mu.Lock()
		if len(r.queue) > 0 {
			r.queue = r.queue[1:]
		}
		r.failStreak = 0
		r.nextRetryAt = time.Time{}
		r.mu.Unlock()

		r.applySyncResponse(ctx, respData)
		sent++
	}
}

func (r *Runner) queueLen() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.queue)
}

func (r *Runner) recordFailure(authRejected bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.failStreak++
	var delay time.Duration
	if authRejected {
		shift := r.failStreak - 1
		if shift > 3 {
			shift = 3
		}
		delay = time.Duration(60<<shift) * time.Second
		if delay > 5*time.Minute {
			delay = 5 * time.Minute
		}
	} else {
		shift := r.failStreak - 1
		if shift > 4 {
			shift = 4
		}
		delay = time.Duration(5<<shift) * time.Second
		if delay > 60*time.Second {
			delay = 60 * time.Second
		}
	}
	r.nextRetryAt = time.Now().Add(delay)
}

func (r *Runner) postReport(ctx context.Context, masterURL, token, cfgMD5 string, rep model.AgentReport) (model.ReportResponse, int, error) {
	var out model.ReportResponse
	raw, err := json.Marshal(rep)
	if err != nil {
		return out, 0, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, masterURL, bytes.NewReader(raw))
	if err != nil {
		return out, 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Agent-Version", r.version)
	if cfgMD5 != "" {
		req.Header.Set("X-Agent-Config-MD5", cfgMD5)
	}
	if token != "" {
		req.Header.Set("X-Agent-Token", token)
	}
	resp, err := r.client.Do(req)
	if err != nil {
		return out, 0, err
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 256<<10))
	if resp.StatusCode >= 200 && resp.StatusCode < 300 && len(body) > 0 {
		_ = json.Unmarshal(body, &out)
	}
	return out, resp.StatusCode, nil
}

func (r *Runner) applySyncResponse(ctx context.Context, resp model.ReportResponse) {
	if resp.ConfigMD5 == "" || resp.AgentConfig == nil {
		return
	}
	r.mu.Lock()
	if resp.ConfigMD5 == r.cfgMD5 {
		r.mu.Unlock()
		return
	}
	syncCfg := resp.AgentConfig
	changed := false

	if syncCfg.IntervalSec >= 5 && syncCfg.IntervalSec != r.cfg.LocalCollector.IntervalSec {
		r.cfg.LocalCollector.IntervalSec = syncCfg.IntervalSec
		changed = true
		select {
		case r.intervalCh <- time.Duration(syncCfg.IntervalSec) * time.Second:
		default:
		}
	}
	if syncCfg.WatchPorts != nil {
		r.cfg.LocalCollector.WatchPorts = append([]int(nil), syncCfg.WatchPorts...)
		changed = true
	}
	if syncCfg.LocalTargets != nil {
		r.cfg.LocalCollector.LocalTargets = append([]model.TargetDef(nil), syncCfg.LocalTargets...)
		changed = true
	}
	if syncCfg.AutoUpdate != nil && *syncCfg.AutoUpdate != r.cfg.LocalCollector.AutoUpdate {
		r.cfg.LocalCollector.AutoUpdate = *syncCfg.AutoUpdate
		changed = true
	}
	if syncCfg.UpdateRepo != "" && syncCfg.UpdateRepo != r.cfg.LocalCollector.UpdateRepo {
		r.cfg.LocalCollector.UpdateRepo = syncCfg.UpdateRepo
		changed = true
	}
	if syncCfg.UpdateProxy != nil && *syncCfg.UpdateProxy != r.cfg.LocalCollector.UpdateProxy {
		r.cfg.LocalCollector.UpdateProxy = *syncCfg.UpdateProxy
		changed = true
	}

	r.cfgMD5 = resp.ConfigMD5
	targets := r.cfg.LocalCollector.LocalTargets
	if len(targets) == 0 {
		targets = config.DefaultLocalTargets()
	}
	watchPorts := r.cfg.LocalCollector.WatchPorts
	cfgPath := r.cfgPath
	cfgSnapshot := *r.cfg
	autoUpdate := r.cfg.LocalCollector.AutoUpdate
	updateRepo := r.cfg.LocalCollector.UpdateRepo
	updateProxy := r.cfg.LocalCollector.UpdateProxy
	r.mu.Unlock()

	r.col.UpdateRuntime(watchPorts, targets)
	if changed && cfgPath != "" {
		if err := config.SaveAtomic(cfgPath, &cfgSnapshot); err != nil {
			log.Printf("save synced config err: %v", err)
		} else {
			log.Printf("agent config synced from master (md5=%s)", resp.ConfigMD5)
		}
	}

	if syncCfg.TargetVersion != "" && syncCfg.TargetVersion != r.version {
		go func() {
			if err := r.updater.TriggerTargetUpdate(ctx, updateRepo, updateProxy, syncCfg.TargetVersion); err != nil {
				log.Printf("target update %s skipped/failed: %v", syncCfg.TargetVersion, err)
			}
		}()
	} else if autoUpdate {
		go func() {
			if err := r.updater.CheckAndSchedule(ctx, updateRepo, updateProxy, false); err != nil {
				log.Printf("auto update check: %v", err)
			}
		}()
	}
}

func (r *Runner) autoUpdateLoop(ctx context.Context) {
	select {
	case <-ctx.Done():
		return
	case <-time.After(45 * time.Second):
	}
	checkOnce := func() {
		r.mu.Lock()
		enabled := r.cfg.LocalCollector.AutoUpdate
		repo := r.cfg.LocalCollector.UpdateRepo
		proxy := r.cfg.LocalCollector.UpdateProxy
		r.mu.Unlock()
		if !enabled {
			return
		}
		if err := r.updater.CheckAndSchedule(ctx, repo, proxy, false); err != nil {
			log.Printf("auto update check: %v", err)
		}
	}
	checkOnce()
	tk := time.NewTicker(6 * time.Hour)
	defer tk.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tk.C:
			checkOnce()
		}
	}
}

// FormatSyncSummary returns a readable status string for logs/tests.
func (r *Runner) FormatSyncSummary() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return fmt.Sprintf("version=%s interval=%ds md5=%s queued=%d", r.version, r.cfg.LocalCollector.IntervalSec, r.cfgMD5, len(r.queue))
}
