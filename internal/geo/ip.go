package geo

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"
)

type ipAPIResp struct {
	Status     string `json:"status"`
	Country    string `json:"country"`
	RegionName string `json:"regionName"`
	City       string `json:"city"`
	ISP        string `json:"isp"`
}

func Lookup(ip string) string {
	parsed := net.ParseIP(strings.TrimSpace(ip))
	if parsed == nil {
		return ""
	}
	if parsed.IsLoopback() || parsed.IsPrivate() {
		return "内网"
	}
	client := &http.Client{Timeout: 3 * time.Second}
	url := fmt.Sprintf("http://ip-api.com/json/%s?lang=zh-CN&fields=status,country,regionName,city,isp", ip)
	resp, err := client.Get(url)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	var d ipAPIResp
	if err := json.NewDecoder(resp.Body).Decode(&d); err != nil || d.Status != "success" {
		return ""
	}

	loc := ""
	if d.Country == "中国" {
		reg := strings.TrimSuffix(d.RegionName, "省")
		city := strings.TrimSuffix(d.City, "市")
		if reg != "" && city != "" && reg != city {
			loc = reg + city
		} else if city != "" {
			loc = city
		} else {
			loc = reg
		}
	} else {
		loc = strings.TrimSpace(d.Country + " " + d.City)
	}

	isp := shortISP(d.ISP)
	if loc != "" && isp != "" {
		return loc + " · " + isp
	}
	if loc != "" {
		return loc
	}
	return isp
}

func shortISP(s string) string {
	low := strings.ToLower(s)
	switch {
	case strings.Contains(low, "chinanet") || strings.Contains(low, "telecom") || strings.Contains(s, "电信"):
		return "电信"
	case strings.Contains(low, "mobile") || strings.Contains(low, "cmnet") || strings.Contains(s, "移动"):
		return "移动"
	case strings.Contains(low, "unicom") || strings.Contains(s, "联通"):
		return "联通"
	case strings.Contains(low, "tencent"):
		return "腾讯云"
	case strings.Contains(low, "alibaba") || strings.Contains(low, "aliyun"):
		return "阿里云"
	default:
		if len([]rune(s)) > 10 {
			return string([]rune(s)[:10])
		}
		return s
	}
}
