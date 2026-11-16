package probe

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"netwatch/internal/model"
)

type gpRequest struct {
	Target             string       `json:"target"`
	Type               string       `json:"type"`
	Locations          []gpLocation `json:"locations"`
	MeasurementOptions gpOptions    `json:"measurementOptions"`
}

type gpLocation struct {
	Country string `json:"country"`
	City    string `json:"city,omitempty"`
	Limit   int    `json:"limit,omitempty"`
}

type gpOptions struct {
	Protocol string `json:"protocol,omitempty"`
	Port     int    `json:"port,omitempty"`
	Packets  int    `json:"packets,omitempty"`
}

type gpCreateResp struct {
	ID string `json:"id"`
}

type gpMeasureResp struct {
	Status  string `json:"status"`
	Results []struct {
		Probe struct {
			City    string `json:"city"`
			Network string `json:"network"`
		} `json:"probe"`
		Result struct {
			Status string `json:"status"`
			Stats  struct {
				Min  float64 `json:"min"`
				Avg  float64 `json:"avg"`
				Max  float64 `json:"max"`
				Loss float64 `json:"loss"`
			} `json:"stats"`
		} `json:"result"`
	} `json:"results"`
}

// MeasureGuangdong calls Globalping probes in Guangzhou & Shenzhen.
// For UDP-only protocols (e.g. Hysteria2) without a TCP probePort, it falls back to ICMP ping.
func MeasureGuangdong(ctx context.Context, target string, port int, probePort int, protocol string, token string) ([]model.ProbeEntry, error) {
	usePort := port
	if probePort > 0 {
		usePort = probePort
	}
	lowProto := strings.ToLower(protocol)
	opts := gpOptions{Packets: 3}
	if probePort == 0 && (strings.Contains(lowProto, "hysteria") || strings.Contains(lowProto, "tuic") || strings.Contains(lowProto, "wireguard")) {
		opts.Protocol = "ICMP"
	} else if usePort > 0 {
		opts.Protocol = "TCP"
		opts.Port = usePort
	}

	reqBody := gpRequest{
		Target: target,
		Type:   "ping",
		Locations: []gpLocation{
			{Country: "CN", City: "Guangzhou", Limit: 3},
			{Country: "CN", City: "Shenzhen", Limit: 3},
		},
		MeasurementOptions: opts,
	}
	raw, err := json.Marshal(reqBody)
	if err != nil {
		return nil, err
	}

	client := &http.Client{Timeout: 15 * time.Second}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.globalping.io/v1/measurements", bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("globalping create %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var created gpCreateResp
	if err := json.Unmarshal(body, &created); err != nil || created.ID == "" {
		return nil, fmt.Errorf("globalping invalid create response: %s", string(body))
	}

	var result gpMeasureResp
	pollURL := "https://api.globalping.io/v1/measurements/" + created.ID
	for i := 0; i < 12; i++ {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(1 * time.Second):
		}
		preq, _ := http.NewRequestWithContext(ctx, http.MethodGet, pollURL, nil)
		if token != "" {
			preq.Header.Set("Authorization", "Bearer "+token)
		}
		presp, err := client.Do(preq)
		if err != nil {
			continue
		}
		pbody, _ := io.ReadAll(presp.Body)
		presp.Body.Close()
		if err := json.Unmarshal(pbody, &result); err != nil {
			continue
		}
		if result.Status == "finished" {
			break
		}
	}

	now := time.Now()
	out := make([]model.ProbeEntry, 0, len(result.Results))
	for _, r := range result.Results {
		isp := shortISP(r.Probe.Network)
		name := fmt.Sprintf("广东%s · %s", shortCity(r.Probe.City), isp)
		avg := r.Result.Stats.Avg
		loss := r.Result.Stats.Loss
		status := "ok"
		if r.Result.Status != "finished" || avg <= 0 || loss >= 100 {
			status = "down"
			avg = 0
		} else if avg > 220 || loss > 10 {
			status = "slow"
		}
		out = append(out, model.ProbeEntry{
			Name:      name,
			Tag:       "gd",
			LatencyMs: avg,
			LossPct:   loss,
			Status:    status,
			UpdatedAt: now,
		})
	}
	return out, nil
}

// TCPing runs local TCP handshake latency probe (3 attempts) from the VPS itself.
func TCPing(name, tag, addr string) model.ProbeEntry {
	const attempts = 3
	var total time.Duration
	var ok int
	for i := 0; i < attempts; i++ {
		start := time.Now()
		conn, err := net.DialTimeout("tcp", addr, 2500*time.Millisecond)
		if err == nil {
			total += time.Since(start)
			ok++
			conn.Close()
		}
		if i < attempts-1 {
			time.Sleep(100 * time.Millisecond)
		}
	}
	now := time.Now()
	if ok == 0 {
		return model.ProbeEntry{
			Name:      name,
			Tag:       tag,
			LatencyMs: 0,
			LossPct:   100,
			Status:    "down",
			UpdatedAt: now,
		}
	}
	avgMs := float64(total.Microseconds()) / float64(ok) / 1000.0
	loss := float64(attempts-ok) / float64(attempts) * 100.0
	status := "ok"
	if avgMs > 220 || loss > 10 {
		status = "slow"
	}
	return model.ProbeEntry{
		Name:      name,
		Tag:       tag,
		LatencyMs: avgMs,
		LossPct:   loss,
		Status:    status,
		UpdatedAt: now,
	}
}

func shortCity(city string) string {
	switch strings.ToLower(city) {
	case "guangzhou":
		return "广州"
	case "shenzhen":
		return "深圳"
	case "taishan":
		return "台山"
	default:
		return city
	}
}

func shortISP(netName string) string {
	low := strings.ToLower(netName)
	switch {
	case strings.Contains(low, "mobile"):
		return "移动"
	case strings.Contains(low, "telecom") || strings.Contains(low, "chinanet"):
		return "电信"
	case strings.Contains(low, "unicom"):
		return "联通"
	case strings.Contains(low, "tencent"):
		return "腾讯云"
	case strings.Contains(low, "alibaba"):
		return "阿里云"
	case strings.Contains(low, "topway"):
		return "天威视讯"
	default:
		if len(netName) > 12 {
			return netName[:12]
		}
		return netName
	}
}
