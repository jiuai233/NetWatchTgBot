package probe

import (
	"net"
	"sort"
	"sync"
	"time"

	"netwatch/internal/model"
)

const (
	DefaultWindowDuration   = 2 * time.Minute
	DefaultWindowMaxSamples = 6
	DefaultProbeDialTimeout = 1500 * time.Millisecond
)

type probeSample struct {
	at    time.Time
	rttMs float64
	ok    bool
}

type targetHistory struct {
	addr    string
	samples []probeSample
}

// RollingWindow maintains a sliding window of recent TCPing samples per target
// and outputs median RTT + window packet loss to smooth transient jitter.
type RollingWindow struct {
	mu         sync.Mutex
	window     time.Duration
	maxSamples int
	dialFn     func(addr string, timeout time.Duration) (float64, bool)
	entries    map[string]*targetHistory
}

func NewRollingWindow() *RollingWindow {
	return &RollingWindow{
		window:     DefaultWindowDuration,
		maxSamples: DefaultWindowMaxSamples,
		dialFn:     tcpDialOnce,
		entries:    make(map[string]*targetHistory),
	}
}

func tcpDialOnce(addr string, timeout time.Duration) (float64, bool) {
	start := time.Now()
	conn, err := net.DialTimeout("tcp", addr, timeout)
	if err != nil {
		return 0, false
	}
	_ = conn.Close()
	ms := float64(time.Since(start).Microseconds()) / 1000.0
	if ms < 1 {
		ms = 1
	}
	return ms, true
}

func (rw *RollingWindow) ProbeAll(targets []model.TargetDef, now time.Time) {
	if len(targets) == 0 {
		return
	}
	type res struct {
		t     model.TargetDef
		rttMs float64
		ok    bool
	}
	results := make([]res, len(targets))
	var wg sync.WaitGroup
	for i, t := range targets {
		wg.Add(1)
		go func(idx int, tgt model.TargetDef) {
			defer wg.Done()
			ms, ok := rw.dialFn(tgt.Addr, DefaultProbeDialTimeout)
			results[idx] = res{t: tgt, rttMs: ms, ok: ok}
		}(i, t)
	}
	wg.Wait()

	rw.mu.Lock()
	defer rw.mu.Unlock()
	for _, r := range results {
		rw.recordLocked(r.t, now, r.rttMs, r.ok)
	}
}

func (rw *RollingWindow) recordLocked(t model.TargetDef, now time.Time, rttMs float64, ok bool) {
	h, exists := rw.entries[t.Name]
	if !exists || h.addr != t.Addr {
		h = &targetHistory{addr: t.Addr}
		rw.entries[t.Name] = h
	}
	h.samples = append(h.samples, probeSample{at: now, rttMs: rttMs, ok: ok})
	if len(h.samples) > rw.maxSamples {
		h.samples = h.samples[len(h.samples)-rw.maxSamples:]
	}
}

func (rw *RollingWindow) Snapshot(targets []model.TargetDef, now time.Time) []model.ProbeEntry {
	rw.mu.Lock()
	defer rw.mu.Unlock()

	cutoff := now.Add(-rw.window)
	out := make([]model.ProbeEntry, 0, len(targets))
	for _, t := range targets {
		h, exists := rw.entries[t.Name]
		if !exists || h.addr != t.Addr || len(h.samples) == 0 {
			continue
		}
		var rtts []float64
		total := 0
		lost := 0
		for _, s := range h.samples {
			if s.at.Before(cutoff) {
				continue
			}
			total++
			if s.ok && s.rttMs > 0 {
				rtts = append(rtts, s.rttMs)
			} else {
				lost++
			}
		}
		if total == 0 {
			continue
		}
		loss := float64(lost*100) / float64(total)
		if len(rtts) == 0 {
			out = append(out, model.ProbeEntry{
				Name:      t.Name,
				Tag:       t.Tag,
				LatencyMs: 0,
				LossPct:   100,
				Status:    "down",
				UpdatedAt: now,
			})
			continue
		}
		med := medianFloat(rtts)
		status := "ok"
		if med > 220 || loss > 10 {
			status = "slow"
		}
		out = append(out, model.ProbeEntry{
			Name:      t.Name,
			Tag:       t.Tag,
			LatencyMs: med,
			LossPct:   loss,
			Status:    status,
			UpdatedAt: now,
		})
	}
	return out
}

func medianFloat(vals []float64) float64 {
	sort.Float64s(vals)
	n := len(vals)
	if n == 0 {
		return 0
	}
	if n%2 == 1 {
		return vals[n/2]
	}
	return (vals[n/2-1] + vals[n/2]) / 2.0
}
