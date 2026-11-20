package collector

import (
	"bufio"
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"netwatch/internal/model"
	"netwatch/internal/probe"
)

type Options struct {
	NodeID       string
	Interface    string   // empty = auto-detect non-lo interfaces
	WatchPorts   []int    // proxy ports to inspect active TCP/UDP peer IPs
	ClashAPIURL  string   // optional e.g. http://127.0.0.1:9090
	ClashSecret  string   // optional
	LocalTargets []Target // optional local probes e.g. Cloudflare DNS, Google DNS
}

type Target = model.TargetDef

type Collector struct {
	opts      Options
	mu        sync.Mutex
	lastCPU   cpuTimes
	hasCPU    bool
	lastNet   netBytes
	lastNetAt time.Time
	lastIP    map[string]ipSnap
	client    *http.Client
	probeWin  *probe.RollingWindow
}

type ipSnap struct {
	rx int64
	tx int64
	at time.Time
}

type cpuTimes struct {
	idle  uint64
	total uint64
}

type netBytes struct {
	rx int64
	tx int64
}

func New(opts Options) *Collector {
	return &Collector{
		opts:     opts,
		lastIP:   make(map[string]ipSnap),
		client:   &http.Client{Timeout: 3 * time.Second},
		probeWin: probe.NewRollingWindow(),
	}
}

func (c *Collector) UpdateRuntime(watchPorts []int, targets []Target) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if watchPorts != nil {
		cp := make([]int, len(watchPorts))
		copy(cp, watchPorts)
		c.opts.WatchPorts = cp
	}
	if targets != nil {
		cp := make([]Target, len(targets))
		copy(cp, targets)
		c.opts.LocalTargets = cp
	}
}

func (c *Collector) currentTargets() []Target {
	c.mu.Lock()
	defer c.mu.Unlock()
	cp := make([]Target, len(c.opts.LocalTargets))
	copy(cp, c.opts.LocalTargets)
	return cp
}

func (c *Collector) currentWatchPorts() []int {
	c.mu.Lock()
	defer c.mu.Unlock()
	cp := make([]int, len(c.opts.WatchPorts))
	copy(cp, c.opts.WatchPorts)
	return cp
}

func (c *Collector) StartProbeLoop(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = 20 * time.Second
	}
	c.probeWin.ProbeAll(c.currentTargets(), time.Now())
	tk := time.NewTicker(interval)
	defer tk.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-tk.C:
			c.probeWin.ProbeAll(c.currentTargets(), now)
		}
	}
}

func (c *Collector) Collect(withProbes bool) model.AgentReport {
	now := time.Now()
	cpu := c.readCPU()
	mem := readMemPct()
	disk := readDiskPct("/")
	rxTotal, txTotal := readNetBytes(c.opts.Interface)

	var rxBps, txBps float64
	c.mu.Lock()
	if !c.lastNetAt.IsZero() {
		dt := now.Sub(c.lastNetAt).Seconds()
		if dt > 0 && rxTotal >= c.lastNet.rx && txTotal >= c.lastNet.tx {
			rxBps = float64(rxTotal-c.lastNet.rx) / dt
			txBps = float64(txTotal-c.lastNet.tx) / dt
		}
	}
	c.lastNet = netBytes{rx: rxTotal, tx: txTotal}
	c.lastNetAt = now
	c.mu.Unlock()

	clients, totalConns := c.collectClients()

	var probes []model.ProbeEntry
	if withProbes {
		targets := c.currentTargets()
		probes = c.probeWin.Snapshot(targets, now)
		if len(probes) == 0 && len(targets) > 0 {
			c.probeWin.ProbeAll(targets, now)
			probes = c.probeWin.Snapshot(targets, now)
		}
	}

	return model.AgentReport{
		NodeID:      c.opts.NodeID,
		Timestamp:   now,
		CPUPct:      cpu,
		MemPct:      mem,
		DiskPct:     disk,
		NetRxTotal:  rxTotal,
		NetTxTotal:  txTotal,
		RxBps:       rxBps,
		TxBps:       txBps,
		ActiveConns: totalConns,
		Probes:      probes,
		Clients:     clients,
	}
}

func (c *Collector) collectClients() ([]model.ClientSample, int) {
	byIP := make(map[string]*model.ClientSample)
	totalConns := 0

	// 1) Try Clash API /connections if configured (has per-IP traffic and speeds)
	if c.opts.ClashAPIURL != "" {
		if clashClients, n, ok := c.readClashConnections(); ok {
			for _, cl := range clashClients {
				cp := cl
				byIP[cl.IP] = &cp
			}
			totalConns = n
		}
	}

	// 2) Also inspect /proc/net/tcp* & udp* for watched ports (covers Hysteria2 UDP or when Clash API lacks inbound)
	watchPorts := c.currentWatchPorts()
	if len(watchPorts) > 0 {
		portSet := make(map[int]bool, len(watchPorts))
		for _, p := range watchPorts {
			portSet[p] = true
		}
		procCounts := readProcSocketPeers(portSet)
		var procTotal int
		for ip, cnt := range procCounts {
			procTotal += cnt
			if existing, ok := byIP[ip]; ok {
				if cnt > existing.ActiveConns {
					existing.ActiveConns = cnt
				}
			} else {
				byIP[ip] = &model.ClientSample{IP: ip, ActiveConns: cnt}
			}
		}
		if procTotal > totalConns {
			totalConns = procTotal
		}
	}

	out := make([]model.ClientSample, 0, len(byIP))
	for _, v := range byIP {
		out = append(out, *v)
	}
	return out, totalConns
}

type clashConnResp struct {
	Connections []struct {
		Upload   int64 `json:"upload"`
		Download int64 `json:"download"`
		Metadata struct {
			SourceIP string `json:"sourceIP"`
		} `json:"metadata"`
	} `json:"connections"`
}

func (c *Collector) readClashConnections() ([]model.ClientSample, int, bool) {
	url := strings.TrimRight(c.opts.ClashAPIURL, "/") + "/connections"
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, 0, false
	}
	if c.opts.ClashSecret != "" {
		req.Header.Set("Authorization", "Bearer "+c.opts.ClashSecret)
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, 0, false
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return nil, 0, false
	}
	var payload clashConnResp
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, 0, false
	}
	agg := make(map[string]*model.ClientSample)
	for _, conn := range payload.Connections {
		ip := strings.TrimSpace(conn.Metadata.SourceIP)
		if ip == "" {
			continue
		}
		item, ok := agg[ip]
		if !ok {
			item = &model.ClientSample{IP: ip}
			agg[ip] = item
		}
		item.ActiveConns++
		item.SessionRx += conn.Download
		item.SessionTx += conn.Upload
	}
	now := time.Now()
	c.mu.Lock()
	for ip, item := range agg {
		if prev, ok := c.lastIP[ip]; ok {
			dt := now.Sub(prev.at).Seconds()
			if dt > 0 {
				if item.SessionRx >= prev.rx {
					item.RxBps = float64(item.SessionRx-prev.rx) / dt
				} else {
					item.RxBps = float64(item.SessionRx) / dt
				}
				if item.SessionTx >= prev.tx {
					item.TxBps = float64(item.SessionTx-prev.tx) / dt
				} else {
					item.TxBps = float64(item.SessionTx) / dt
				}
			}
		}
		c.lastIP[ip] = ipSnap{rx: item.SessionRx, tx: item.SessionTx, at: now}
	}
	c.mu.Unlock()

	out := make([]model.ClientSample, 0, len(agg))
	for _, item := range agg {
		out = append(out, *item)
	}
	return out, len(payload.Connections), true
}

func (c *Collector) readCPU() float64 {
	f, err := os.Open("/proc/stat")
	if err != nil {
		return 0
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	if !sc.Scan() {
		return 0
	}
	fields := strings.Fields(sc.Text())
	if len(fields) < 5 || fields[0] != "cpu" {
		return 0
	}
	var total, idle uint64
	for i := 1; i < len(fields); i++ {
		v, _ := strconv.ParseUint(fields[i], 10, 64)
		total += v
		if i == 4 || i == 5 { // idle + iowait
			idle += v
		}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.hasCPU {
		c.lastCPU = cpuTimes{idle: idle, total: total}
		c.hasCPU = true
		return 0
	}
	dt := total - c.lastCPU.total
	di := idle - c.lastCPU.idle
	c.lastCPU = cpuTimes{idle: idle, total: total}
	if dt == 0 {
		return 0
	}
	return float64(dt-di) / float64(dt) * 100.0
}

func readMemPct() float64 {
	// Prefer cgroup limit in LXC/Docker containers so host memory doesn't skew readings.
	if cur, ok1 := readIntFile("/sys/fs/cgroup/memory.current"); ok1 {
		if max, ok2 := readIntFile("/sys/fs/cgroup/memory.max"); ok2 && max > 0 && cur > 0 {
			return float64(cur) / float64(max) * 100.0
		}
	}
	if cur, ok1 := readIntFile("/sys/fs/cgroup/memory/memory.usage_in_bytes"); ok1 {
		if max, ok2 := readIntFile("/sys/fs/cgroup/memory/memory.limit_in_bytes"); ok2 && max > 0 && max < (1<<50) && cur > 0 {
			return float64(cur) / float64(max) * 100.0
		}
	}

	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return 0
	}
	defer f.Close()
	var total, avail float64
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, "MemTotal:") {
			total = parseMemVal(line)
		} else if strings.HasPrefix(line, "MemAvailable:") {
			avail = parseMemVal(line)
		}
	}
	if total <= 0 {
		return 0
	}
	return (total - avail) / total * 100.0
}

func parseMemVal(line string) float64 {
	fields := strings.Fields(line)
	if len(fields) < 2 {
		return 0
	}
	v, _ := strconv.ParseFloat(fields[1], 64)
	return v
}

func readIntFile(path string) (int64, bool) {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0, false
	}
	s := strings.TrimSpace(string(b))
	v, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0, false
	}
	return v, true
}

func readDiskPct(path string) float64 {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil || st.Blocks == 0 {
		return 0
	}
	used := st.Blocks - st.Bfree
	return float64(used) / float64(st.Blocks) * 100.0
}

func readNetBytes(iface string) (rx, tx int64) {
	f, err := os.Open("/proc/net/dev")
	if err != nil {
		return 0, 0
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		idx := strings.IndexByte(line, ':')
		if idx <= 0 {
			continue
		}
		name := strings.TrimSpace(line[:idx])
		if name == "lo" || strings.HasPrefix(name, "docker") || strings.HasPrefix(name, "veth") {
			continue
		}
		if iface != "" && name != iface {
			continue
		}
		fields := strings.Fields(line[idx+1:])
		if len(fields) < 9 {
			continue
		}
		r, _ := strconv.ParseInt(fields[0], 10, 64)
		t, _ := strconv.ParseInt(fields[8], 10, 64)
		rx += r
		tx += t
	}
	return rx, tx
}

func readProcSocketPeers(watchPorts map[int]bool) map[string]int {
	counts := make(map[string]int)
	files := []struct {
		path string
		v6   bool
		tcp  bool
	}{
		{"/proc/net/tcp", false, true},
		{"/proc/net/tcp6", true, true},
		{"/proc/net/udp", false, false},
		{"/proc/net/udp6", true, false},
	}
	for _, spec := range files {
		f, err := os.Open(spec.path)
		if err != nil {
			continue
		}
		sc := bufio.NewScanner(f)
		sc.Scan() // header
		for sc.Scan() {
			fields := strings.Fields(sc.Text())
			if len(fields) < 4 {
				continue
			}
			// TCP established only (state == "01"), UDP connected peers (state == "01")
			if fields[3] != "01" {
				continue
			}
			_, lport := parseHexAddr(fields[1], spec.v6)
			if !watchPorts[lport] {
				continue
			}
			rip, rport := parseHexAddr(fields[2], spec.v6)
			if rport == 0 || rip == "" || rip == "0.0.0.0" || rip == "::" || rip == "127.0.0.1" {
				continue
			}
			counts[rip]++
		}
		f.Close()
	}
	return counts
}

func parseHexAddr(s string, v6 bool) (string, int) {
	parts := strings.Split(s, ":")
	if len(parts) != 2 {
		return "", 0
	}
	portVal, err := strconv.ParseInt(parts[1], 16, 32)
	if err != nil {
		return "", 0
	}
	raw, err := hex.DecodeString(parts[0])
	if err != nil {
		return "", int(portVal)
	}
	if !v6 && len(raw) == 4 {
		ip := net.IPv4(raw[3], raw[2], raw[1], raw[0]).String()
		return ip, int(portVal)
	}
	if v6 && len(raw) == 16 {
		// Linux /proc/net/tcp6 stores 4 little-endian 32-bit words
		ipBytes := make([]byte, 16)
		for w := 0; w < 4; w++ {
			ipBytes[w*4+0] = raw[w*4+3]
			ipBytes[w*4+1] = raw[w*4+2]
			ipBytes[w*4+2] = raw[w*4+1]
			ipBytes[w*4+3] = raw[w*4+0]
		}
		ip := net.IP(ipBytes)
		if v4 := ip.To4(); v4 != nil {
			return v4.String(), int(portVal)
		}
		return ip.String(), int(portVal)
	}
	return "", int(portVal)
}

func FormatBytes(b int64) string {
	const (
		KB = 1024
		MB = 1024 * KB
		GB = 1024 * MB
		TB = 1024 * GB
	)
	switch {
	case b >= TB:
		return fmt.Sprintf("%.2f TB", float64(b)/TB)
	case b >= GB:
		return fmt.Sprintf("%.2f GB", float64(b)/GB)
	case b >= MB:
		return fmt.Sprintf("%.1f MB", float64(b)/MB)
	case b >= KB:
		return fmt.Sprintf("%.0f KB", float64(b)/KB)
	default:
		return fmt.Sprintf("%d B", b)
	}
}
