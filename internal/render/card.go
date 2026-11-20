package render

import (
	"fmt"
	"image/color"
	"math"
	"os"
	"strings"
	"sync"

	"netwatch/internal/collector"
	"netwatch/internal/model"

	"github.com/fogleman/gg"
	"golang.org/x/image/font"
	"golang.org/x/image/font/basicfont"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/font/sfnt"
)

var (
	bgPage      = color.RGBA{246, 244, 238, 255} // #F6F4EE
	bgCard      = color.RGBA{255, 255, 255, 255}
	bgSoft      = color.RGBA{249, 247, 242, 255}
	borderCol   = color.RGBA{226, 220, 208, 255}
	textMain    = color.RGBA{34, 32, 29, 255}
	textMuted   = color.RGBA{120, 114, 105, 255}
	colBlue     = color.RGBA{65, 118, 236, 255}
	colBlueSoft = color.NRGBA{65, 118, 236, 42}
	colGreen    = color.RGBA{34, 158, 92, 255}
	colAmber    = color.RGBA{189, 133, 44, 255}
	colBrown    = color.RGBA{92, 62, 24, 255}
	colRed      = color.RGBA{214, 69, 65, 255}
	colTrack    = color.RGBA{236, 231, 220, 255}
)

var (
	fontOnce sync.Once
	sfntReg  *sfnt.Font
	sfntBold *sfnt.Font
)

func initFonts() {
	fontOnce.Do(func() {
		candidates := []string{
			os.Getenv("NETWATCH_FONT"),
			"./font.ttf",
			"/opt/netwatch/font.ttf",
			"/System/Library/Fonts/STHeiti Medium.ttc",
			"/System/Library/Fonts/Hiragino Sans GB.ttc",
			"/Library/Fonts/Arial Unicode.ttf",
			"/usr/share/fonts/opentype/noto/NotoSansCJK-Medium.ttc",
			"/usr/share/fonts/opentype/noto/NotoSansCJK-Regular.ttc",
			"/usr/share/fonts/noto-cjk/NotoSansCJK-Regular.ttc",
			"/usr/share/fonts/truetype/noto/NotoSansSC-Regular.ttf",
			"/usr/share/fonts/truetype/wqy/wqy-zenhei.ttc",
		}
		for _, p := range candidates {
			if p == "" {
				continue
			}
			f := parseFontFile(p)
			if f != nil {
				sfntBold = f
				break
			}
		}
		lightCandidates := []string{
			os.Getenv("NETWATCH_FONT"),
			"./font.ttf",
			"/opt/netwatch/font.ttf",
			"/System/Library/Fonts/STHeiti Light.ttc",
			"/System/Library/Fonts/Hiragino Sans GB.ttc",
			"/Library/Fonts/Arial Unicode.ttf",
			"/usr/share/fonts/opentype/noto/NotoSansCJK-Regular.ttc",
			"/usr/share/fonts/noto-cjk/NotoSansCJK-Regular.ttc",
			"/usr/share/fonts/truetype/noto/NotoSansSC-Regular.ttf",
			"/usr/share/fonts/truetype/wqy/wqy-zenhei.ttc",
		}
		for _, p := range lightCandidates {
			if p == "" {
				continue
			}
			f := parseFontFile(p)
			if f != nil {
				sfntReg = f
				break
			}
		}
		if sfntReg == nil {
			sfntReg = sfntBold
		}
		if sfntBold == nil {
			sfntBold = sfntReg
		}
	})
}

func parseFontFile(path string) *sfnt.Font {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	if strings.HasSuffix(strings.ToLower(path), ".ttc") {
		col, err := sfnt.ParseCollection(data)
		if err != nil || col.NumFonts() == 0 {
			return nil
		}
		f, err := col.Font(0)
		if err == nil {
			return f
		}
		return nil
	}
	f, err := opentype.Parse(data)
	if err != nil {
		return nil
	}
	return f
}

func face(size float64, bold bool) font.Face {
	initFonts()
	src := sfntReg
	if bold {
		src = sfntBold
	}
	if src == nil {
		return basicfont.Face7x13
	}
	f, err := opentype.NewFace(src, &opentype.FaceOptions{
		Size:    size,
		DPI:     72,
		Hinting: font.HintingFull,
	})
	if err != nil {
		return basicfont.Face7x13
	}
	return f
}

func setFont(dc *gg.Context, size float64, bold bool) {
	dc.SetFontFace(face(size, bold))
}

func drawCard(dc *gg.Context, x, y, w, h, r float64) {
	dc.SetColor(bgCard)
	dc.DrawRoundedRectangle(x, y, w, h, r)
	dc.Fill()
	dc.SetColor(borderCol)
	dc.SetLineWidth(1.2)
	dc.DrawRoundedRectangle(x, y, w, h, r)
	dc.Stroke()
}

func drawSoftBox(dc *gg.Context, x, y, w, h, r float64) {
	dc.SetColor(bgSoft)
	dc.DrawRoundedRectangle(x, y, w, h, r)
	dc.Fill()
	dc.SetColor(borderCol)
	dc.SetLineWidth(1)
	dc.DrawRoundedRectangle(x, y, w, h, r)
	dc.Stroke()
}

func drawProgress(dc *gg.Context, x, y, w, h, pct float64, fill color.Color) {
	if pct < 0 {
		pct = 0
	}
	if pct > 100 {
		pct = 100
	}
	dc.SetColor(colTrack)
	dc.DrawRoundedRectangle(x, y, w, h, h/2)
	dc.Fill()
	if pct > 0 {
		fw := w * pct / 100.0
		if fw < h {
			fw = h
		}
		dc.SetColor(fill)
		dc.DrawRoundedRectangle(x, y, fw, h, h/2)
		dc.Fill()
	}
}

func formatBps(bps float64) string {
	switch {
	case bps >= 1024*1024:
		return fmt.Sprintf("%.1f MB/s", bps/(1024*1024))
	case bps >= 1024:
		return fmt.Sprintf("%.1f KB/s", bps/1024)
	default:
		return fmt.Sprintf("%.0f B/s", bps)
	}
}

func RenderOverviewPNG(v model.OverviewView, outPath string) error {
	const W = 1240.0
	const pad = 28.0

	nodeRows := len(v.Nodes)
	if nodeRows < 2 {
		nodeRows = 2
	}
	clientRows := len(v.TopClients)
	if clientRows < 2 {
		clientRows = 2
	}
	bottomRows := nodeRows
	if clientRows > bottomRows {
		bottomRows = clientRows
	}
	bottomH := 92.0 + float64(bottomRows)*46.0
	H := 760.0 + bottomH

	dc := gg.NewContext(int(W), int(H))
	dc.SetColor(bgPage)
	dc.Clear()

	// Header
	setFont(dc, 24, true)
	dc.SetColor(textMain)
	dc.DrawString(v.Title, pad, pad+24)
	setFont(dc, 13, false)
	dc.SetColor(textMuted)
	dc.DrawString(fmt.Sprintf("统计周期: %s · 更新于 %s UTC+8", v.PeriodLabel, toCST(v.GeneratedAt).Format("2006-01-02 15:04:05")), pad, pad+48)

	// Row 1: Left = 30-day bar chart (width 730), Right = Quota summary (width 434)
	row1Y := pad + 68
	row1H := 330.0
	leftW := 730.0
	rightX := pad + leftW + 20
	rightW := W - rightX - pad

	// 30-day card
	drawCard(dc, pad, row1Y, leftW, row1H, 12)
	setFont(dc, 17, true)
	dc.SetColor(textMain)
	dc.DrawString("近 30 天每日流量", pad+20, row1Y+30)
	setFont(dc, 12, false)
	dc.SetColor(textMuted)
	dc.DrawString("上行(蓝) / 下行(绿)分开，虚线为 7 日均值 · 持久化防重启清零", pad+20, row1Y+50)
	drawDaily30Chart(dc, pad+22, row1Y+70, leftW-44, row1H-95, v.Daily30)

	// Quota summary card (per-node breakdown so separate billing cycles don't mix)
	drawCard(dc, rightX, row1Y, rightW, row1H, 12)
	setFont(dc, 17, true)
	dc.SetColor(textMain)
	dc.DrawString("各节点套餐与配额", rightX+20, row1Y+30)
	setFont(dc, 12, false)
	dc.SetColor(textMuted)
	dc.DrawString(fmt.Sprintf("今日已采 ↑%s · ↓%s", collector.FormatBytes(v.CycleTx), collector.FormatBytes(v.CycleRx)), rightX+20, row1Y+50)

	maxShow := len(v.Nodes)
	if maxShow > 3 {
		maxShow = 3
	}
	ny := row1Y + 78
	for i := 0; i < maxShow; i++ {
		nd := v.Nodes[i]
		pct := 0.0
		if nd.QuotaBytes > 0 {
			pct = float64(nd.UsedBytes) / float64(nd.QuotaBytes) * 100.0
		}
		barCol := colGreen
		if pct > 80 {
			barCol = colRed
		} else if pct > 60 {
			barCol = colAmber
		}

		setFont(dc, 13, true)
		dc.SetColor(textMain)
		dc.DrawString(truncate(nd.Name, 14), rightX+20, ny)

		quotaStr := collector.FormatBytes(nd.UsedBytes)
		if nd.QuotaBytes > 0 {
			quotaStr = fmt.Sprintf("%s / %s (%.1f%%)", collector.FormatBytes(nd.UsedBytes), collector.FormatBytes(nd.QuotaBytes), pct)
		}
		setFont(dc, 12, true)
		dc.SetColor(textMain)
		dc.DrawStringAnchored(quotaStr, rightX+rightW-20, ny, 1, 0)

		drawProgress(dc, rightX+20, ny+10, rightW-40, 8, pct, barCol)
		setFont(dc, 11, false)
		dc.SetColor(textMuted)
		exp := nd.ExpireDate
		if exp == "" {
			exp = "--"
		}
		dc.DrawString(fmt.Sprintf("每月 %d 日重置 · 到期 %s", nd.ResetDay, exp), rightX+20, ny+31)
		ny += 58
	}

	sparkY := ny + 4
	if sparkY < row1Y+row1H-42 {
		drawSpeedSparkline(dc, rightX+20, sparkY, rightW-40, row1Y+row1H-16-sparkY, v.SpeedHistory, true)
	}

	// Row 2: Realtime speed card (full width or left 450 + summary)
	row2Y := row1Y + row1H + 20
	row2H := 230.0
	drawCard(dc, pad, row2Y, W-pad*2, row2H, 12)
	setFont(dc, 17, true)
	dc.SetColor(textMain)
	dc.DrawString("全节点实时总网速", pad+20, row2Y+30)
	setFont(dc, 18, true)
	dc.SetColor(colBlue)
	dc.DrawStringAnchored(formatBps(v.CurrentBps), W-pad-20, row2Y+30, 1, 0)
	setFont(dc, 12, false)
	dc.SetColor(textMuted)
	dc.DrawString("含代理出口与本地采集速率变化曲线", pad+20, row2Y+50)
	drawSpeedSparkline(dc, pad+24, row2Y+68, W-pad*2-48, row2H-86, v.SpeedHistory, false)

	// Row 3: Left = Node usage & Guangdong latency, Right = Active Client IPs & Traffic
	row3Y := row2Y + row2H + 20
	nCardW := 660.0
	cCardX := pad + nCardW + 20
	cCardW := W - cCardX - pad

	// Nodes Card
	drawCard(dc, pad, row3Y, nCardW, bottomH, 12)
	setFont(dc, 17, true)
	dc.SetColor(textMain)
	dc.DrawString("节点用量与广东延迟", pad+20, row3Y+30)
	setFont(dc, 12, false)
	dc.SetColor(textMuted)
	dc.DrawString("节点 / 状态", pad+20, row3Y+60)
	dc.DrawString("用量", pad+250, row3Y+60)
	dc.DrawString("连接", pad+415, row3Y+60)
	dc.DrawString("网速", pad+475, row3Y+60)
	dc.DrawString("广东延迟", pad+565, row3Y+60)

	for i, n := range v.Nodes {
		y := row3Y + 88 + float64(i)*46
		dc.SetColor(borderCol)
		dc.SetLineWidth(0.8)
		dc.DrawLine(pad+20, y-22, pad+nCardW-20, y-22)
		dc.Stroke()

		dotCol := colGreen
		if !n.Online || n.ProxyState == "down" {
			dotCol = colRed
		} else if n.ProxyState == "slow" || n.GDLatencyMs > 220 || n.GDLossPct > 10 {
			dotCol = colAmber
		} else if n.ProxyState == "unknown" || (n.ProxyState == "" && n.GDLatencyMs <= 0) {
			dotCol = textMuted
		}
		dc.SetColor(dotCol)
		dc.DrawCircle(pad+26, y-4, 4.5)
		dc.Fill()

		setFont(dc, 13.5, true)
		dc.SetColor(textMain)
		dc.DrawString(truncate(n.Name, 18), pad+38, y)
		setFont(dc, 11, false)
		dc.SetColor(textMuted)
		dc.DrawString(fmt.Sprintf("%s:%d", n.Protocol, n.Port), pad+38, y+15)

		npct := 0.0
		if n.QuotaBytes > 0 {
			npct = float64(n.UsedBytes) / float64(n.QuotaBytes) * 100.0
		}
		drawProgress(dc, pad+250, y-8, 86, 6, npct, colAmber)
		setFont(dc, 12.5, false)
		dc.SetColor(textMain)
		dc.DrawString(collector.FormatBytes(n.UsedBytes), pad+344, y)

		dc.DrawString(fmt.Sprintf("%d", n.ActiveConns), pad+420, y)
		dc.DrawString(formatBps(n.RxBps+n.TxBps), pad+475, y)

		if !n.Online {
			dc.SetColor(colRed)
			dc.DrawString("Agent 离线", pad+565, y)
		} else if n.ProxyState == "down" {
			dc.SetColor(colRed)
			dc.DrawString("全阻断", pad+565, y)
		} else if n.GDLatencyMs <= 0 {
			dc.SetColor(textMuted)
			dc.DrawString("待测", pad+565, y)
		} else {
			if n.GDLossPct > 10 || n.GDLatencyMs >= 200 {
				dc.SetColor(colRed)
			} else if n.GDLatencyMs >= 110 {
				dc.SetColor(colAmber)
			} else {
				dc.SetColor(colGreen)
			}
			dc.DrawString(fmt.Sprintf("%.0f ms", n.GDLatencyMs), pad+565, y)
		}
	}

	// Client IPs Card
	drawCard(dc, cCardX, row3Y, cCardW, bottomH, 12)
	setFont(dc, 17, true)
	dc.SetColor(textMain)
	dc.DrawString("在线连接 IP 与流量消耗", cCardX+20, row3Y+30)
	setFont(dc, 12, false)
	dc.SetColor(textMuted)
	dc.DrawString("来源 IP", cCardX+20, row3Y+60)
	dc.DrawString("并发", cCardX+230, row3Y+60)
	dc.DrawString("实时速率", cCardX+295, row3Y+60)
	dc.DrawString("累计消耗", cCardX+405, row3Y+60)

	for i, c := range v.TopClients {
		y := row3Y + 88 + float64(i)*46
		dc.SetColor(borderCol)
		dc.SetLineWidth(0.8)
		dc.DrawLine(cCardX+20, y-22, cCardX+cCardW-20, y-22)
		dc.Stroke()

		if c.ActiveConns > 0 {
			dc.SetColor(colGreen)
		} else {
			dc.SetColor(textMuted)
		}
		dc.DrawCircle(cCardX+26, y-4, 4)
		dc.Fill()

		setFont(dc, 13, true)
		dc.SetColor(textMain)
		dc.DrawString(truncate(c.IP, 20), cCardX+36, y)
		sub := c.NodeName
		if c.Region != "" {
			if sub != "" {
				sub += " · " + c.Region
			} else {
				sub = c.Region
			}
		}
		if sub != "" {
			setFont(dc, 10.5, false)
			dc.SetColor(textMuted)
			dc.DrawString(truncate(sub, 26), cCardX+36, y+15)
		}

		setFont(dc, 12.5, false)
		dc.SetColor(textMain)
		dc.DrawString(fmt.Sprintf("%d", c.ActiveConns), cCardX+235, y)
		dc.DrawString(formatBps(c.RxBps+c.TxBps), cCardX+295, y)
		setFont(dc, 12.5, true)
		dc.DrawString(collector.FormatBytes(c.TotalRx+c.TotalTx), cCardX+405, y)
	}

	return dc.SavePNG(outPath)
}

func RenderNodeDetailPNG(v model.NodeDetailView, outPath string) error {
	const W = 980.0
	const pad = 26.0

	probeRows := len(v.Probes)
	if probeRows < 4 {
		probeRows = 4
	}
	clientRows := len(v.ClientIPs)
	if clientRows < 3 {
		clientRows = 3
	}
	dailyRows := len(v.Daily7)
	if dailyRows < 5 {
		dailyRows = 5
	}

	leftH := 70.0 + float64(dailyRows)*34.0
	rightH := 70.0 + float64(probeRows)*34.0
	midRowH := math.Max(leftH, rightH)
	clientH := 78.0 + float64(clientRows)*38.0
	H := 640.0 + midRowH + clientH

	dc := gg.NewContext(int(W), int(H))
	dc.SetColor(bgPage)
	dc.Clear()

	// Title row
	dot := colGreen
	if !v.Node.Online || v.Node.ProxyState == "down" {
		dot = colRed
	} else if v.Node.ProxyState == "slow" || v.Node.GDLatencyMs > 220 || v.Node.GDLossPct > 10 {
		dot = colAmber
	} else if v.Node.ProxyState == "unknown" || (v.Node.ProxyState == "" && v.Node.GDLatencyMs <= 0) {
		dot = textMuted
	}
	dc.SetColor(dot)
	dc.DrawCircle(pad+8, pad+15, 6)
	dc.Fill()
	setFont(dc, 22, true)
	dc.SetColor(textMain)
	dc.DrawString(v.Node.Name, pad+22, pad+22)
	setFont(dc, 13, false)
	dc.SetColor(textMuted)
	dc.DrawStringAnchored(fmt.Sprintf("%s:%d (%s) · %s", v.Node.Host, v.Node.Port, v.Node.Protocol, toCST(v.GeneratedAt).Format("15:04:05")), W-pad, pad+22, 1, 0)

	// Health Card
	y := pad + 42
	drawCard(dc, pad, y, W-pad*2, 92, 12)
	scoreCol := colGreen
	if v.Node.HealthScore < 60 {
		scoreCol = colRed
	} else if v.Node.HealthScore < 85 {
		scoreCol = colAmber
	}
	setFont(dc, 42, true)
	dc.SetColor(scoreCol)
	dc.DrawString(fmt.Sprintf("%d", v.Node.HealthScore), pad+24, y+56)
	setFont(dc, 17, true)
	dc.SetColor(textMain)
	dc.DrawString("综合健康度 · "+v.Node.HealthLabel, pad+104, y+40)
	setFont(dc, 13, false)
	dc.SetColor(textMuted)
	dc.DrawString(v.Node.HealthNote, pad+104, y+64)

	// 4 KPI Boxes
	y += 108
	boxW := (W - pad*2 - 36) / 4
	kpis := []struct {
		Label string
		Val   string
	}{
		{"CPU", fmt.Sprintf("%.0f%%", v.Node.CPUPct)},
		{"内存", fmt.Sprintf("%.0f%%", v.Node.MemPct)},
		{"硬盘", fmt.Sprintf("%.0f%%", v.Node.DiskPct)},
		{"广东延迟", fmt.Sprintf("%.0f ms", v.Node.GDLatencyMs)},
	}
	if !v.Node.Online {
		kpis[3].Val = "Agent 离线"
	} else if v.Node.ProxyState == "down" {
		kpis[3].Val = "全阻断"
	} else if v.Node.GDLatencyMs <= 0 {
		kpis[3].Val = "待测"
	}
	for i, k := range kpis {
		bx := pad + float64(i)*(boxW+12)
		drawCard(dc, bx, y, boxW, 84, 10)
		setFont(dc, 12.5, false)
		dc.SetColor(textMuted)
		dc.DrawString(k.Label, bx+16, y+26)
		setFont(dc, 24, true)
		dc.SetColor(textMain)
		dc.DrawString(k.Val, bx+16, y+62)
	}

	// Resource usage history chart
	y += 100
	resH := 230.0
	drawCard(dc, pad, y, W-pad*2, resH, 12)
	setFont(dc, 17, true)
	dc.SetColor(textMain)
	dc.DrawString("资源使用率 ("+v.WindowLabel+")", pad+20, y+30)
	setFont(dc, 12, false)
	dc.SetColor(colAmber)
	dc.DrawString("● CPU", pad+20, y+54)
	dc.SetColor(colBlue)
	dc.DrawString("● 内存", pad+80, y+54)
	dc.SetColor(colGreen)
	dc.DrawString("● 硬盘", pad+142, y+54)
	drawResourceChart(dc, pad+24, y+70, W-pad*2-48, resH-90, v.ResourceHist)

	// Middle split: Left = Daily 7 days traffic, Right = Probes
	y += resH + 18
	halfW := (W - pad*2 - 18) / 2
	drawCard(dc, pad, y, halfW, midRowH, 12)
	setFont(dc, 17, true)
	dc.SetColor(textMain)
	dc.DrawString("原始上下行日流量", pad+20, y+30)
	setFont(dc, 12, false)
	dc.SetColor(textMuted)
	dc.DrawString("最近 7 日 · 持久化累计", pad+20, y+50)

	var maxDay int64 = 1
	for _, d := range v.Daily7 {
		if d.RxBytes+d.TxBytes > maxDay {
			maxDay = d.RxBytes + d.TxBytes
		}
	}
	for i, d := range v.Daily7 {
		ry := y + 78 + float64(i)*34
		setFont(dc, 12.5, false)
		dc.SetColor(textMuted)
		label := d.Date
		if len(label) >= 10 {
			label = label[5:]
		}
		dc.DrawString(label, pad+20, ry)

		barX := pad + 72
		barW := halfW - 160
		tot := d.RxBytes + d.TxBytes
		dc.SetColor(colTrack)
		dc.DrawRoundedRectangle(barX, ry-10, barW, 10, 4)
		dc.Fill()
		if tot > 0 {
			wTot := barW * float64(tot) / float64(maxDay)
			wRx := wTot * float64(d.RxBytes) / float64(tot)
			if wRx > 0 {
				dc.SetColor(colAmber)
				dc.DrawRoundedRectangle(barX, ry-10, wRx, 10, 4)
				dc.Fill()
			}
			wTx := wTot - wRx
			if wTx > 1 {
				dc.SetColor(colBrown)
				dc.DrawRoundedRectangle(barX+wRx, ry-10, wTx, 10, 4)
				dc.Fill()
			}
		}
		setFont(dc, 12.5, true)
		dc.SetColor(textMain)
		dc.DrawStringAnchored(collector.FormatBytes(tot), pad+halfW-20, ry, 1, 0)
	}

	// Right: Probes
	rx := pad + halfW + 18
	drawCard(dc, rx, y, halfW, midRowH, 12)
	setFont(dc, 17, true)
	dc.SetColor(textMain)
	dc.DrawString("探测地址 (含广东三网)", rx+20, y+30)
	setFont(dc, 12, false)
	dc.SetColor(textMuted)
	dc.DrawString("探针节点", rx+20, y+50)
	dc.DrawStringAnchored("延迟 · 丢包", rx+halfW-20, y+50, 1, 0)

	for i, p := range v.Probes {
		py := y + 78 + float64(i)*34
		setFont(dc, 12.5, false)
		dc.SetColor(textMain)
		dc.DrawString(truncate(p.Name, 26), rx+20, py)

		if p.Status == "down" || p.LatencyMs <= 0 {
			dc.SetColor(colRed)
			dc.DrawStringAnchored("超时 · 丢包 100%", rx+halfW-20, py, 1, 0)
		} else {
			c := colGreen
			if p.LatencyMs > 220 || p.LossPct > 10 {
				c = colAmber
			}
			dc.SetColor(c)
			dc.DrawStringAnchored(fmt.Sprintf("%.0f ms", p.LatencyMs), rx+halfW-92, py, 1, 0)
			dc.SetColor(textMuted)
			dc.DrawStringAnchored(fmt.Sprintf("丢包 %.1f%%", p.LossPct), rx+halfW-20, py, 1, 0)
		}
	}

	// Bottom: Active TCP/UDP Client IPs & Per-IP Persisted Traffic
	y += midRowH + 18
	drawCard(dc, pad, y, W-pad*2, clientH, 12)
	setFont(dc, 17, true)
	dc.SetColor(textMain)
	dc.DrawString("当前连接 IP 与流量消耗排行", pad+20, y+30)
	setFont(dc, 12, false)
	dc.SetColor(textMuted)
	dc.DrawString("客户端 IP", pad+20, y+54)
	dc.DrawString("状态 / 并发", pad+280, y+54)
	dc.DrawString("实时速率", pad+450, y+54)
	dc.DrawString("累计下行", pad+600, y+54)
	dc.DrawString("累计上行", pad+730, y+54)
	dc.DrawStringAnchored("总消耗", W-pad-20, y+54, 1, 0)

	for i, c := range v.ClientIPs {
		cy := y + 84 + float64(i)*36
		dc.SetColor(borderCol)
		dc.SetLineWidth(0.8)
		dc.DrawLine(pad+20, cy-18, W-pad-20, cy-18)
		dc.Stroke()

		setFont(dc, 13, true)
		dc.SetColor(textMain)
		lbl := c.IP
		if c.Region != "" {
			lbl += "  (" + c.Region + ")"
		}
		dc.DrawString(truncate(lbl, 34), pad+20, cy)

		if c.ActiveConns > 0 {
			dc.SetColor(colGreen)
			dc.DrawString(fmt.Sprintf("在线 · %d 连接", c.ActiveConns), pad+280, cy)
		} else {
			dc.SetColor(textMuted)
			dc.DrawString("离线历史", pad+280, cy)
		}
		dc.SetColor(textMain)
		setFont(dc, 12.5, false)
		dc.DrawString(formatBps(c.RxBps+c.TxBps), pad+450, cy)
		dc.DrawString(collector.FormatBytes(c.TotalRx), pad+600, cy)
		dc.DrawString(collector.FormatBytes(c.TotalTx), pad+730, cy)
		setFont(dc, 12.5, true)
		dc.DrawStringAnchored(collector.FormatBytes(c.TotalRx+c.TotalTx), W-pad-20, cy, 1, 0)
	}

	return dc.SavePNG(outPath)
}

func drawDaily30Chart(dc *gg.Context, x, y, w, h float64, data []model.DailyTraffic) {
	if len(data) == 0 {
		return
	}
	var maxVal int64 = 1
	for _, d := range data {
		if d.RxBytes+d.TxBytes > maxVal {
			maxVal = d.RxBytes + d.TxBytes
		}
	}
	plotX := x + 46
	plotW := w - 52
	plotH := h - 26

	// Y grid
	setFont(dc, 10.5, false)
	for s := 0; s <= 4; s++ {
		gy := y + plotH - plotH*float64(s)/4.0
		dc.SetColor(color.RGBA{235, 231, 222, 255})
		dc.SetLineWidth(1)
		dc.DrawLine(plotX, gy, plotX+plotW, gy)
		dc.Stroke()
		val := float64(maxVal) * float64(s) / 4.0
		dc.SetColor(textMuted)
		dc.DrawStringAnchored(collector.FormatBytes(int64(val)), plotX-6, gy+3, 1, 0)
	}

	step := plotW / float64(len(data))
	barW := step * 0.68
	if barW > 18 {
		barW = 18
	}

	maPts := make([][2]float64, 0, len(data))
	for i, d := range data {
		cx := plotX + (float64(i)+0.5)*step
		bx := cx - barW/2
		txH := plotH * float64(d.TxBytes) / float64(maxVal)
		rxH := plotH * float64(d.RxBytes) / float64(maxVal)

		if txH > 0 {
			dc.SetColor(colBlue)
			dc.DrawRectangle(bx, y+plotH-txH, barW, txH)
			dc.Fill()
		}
		if rxH > 0 {
			dc.SetColor(colGreen)
			dc.DrawRectangle(bx, y+plotH-txH-rxH, barW, rxH)
			dc.Fill()
		}

		// 7-day moving average
		start := i - 6
		if start < 0 {
			start = 0
		}
		var sum int64
		for j := start; j <= i; j++ {
			sum += data[j].RxBytes + data[j].TxBytes
		}
		avg := float64(sum) / float64(i-start+1)
		ay := y + plotH - plotH*avg/float64(maxVal)
		maPts = append(maPts, [2]float64{cx, ay})

		if i%3 == 0 || i == len(data)-1 {
			lbl := d.Date
			if len(lbl) >= 10 {
				lbl = lbl[5:]
			}
			setFont(dc, 9.5, false)
			dc.SetColor(textMuted)
			dc.DrawStringAnchored(lbl, cx, y+plotH+15, 0.5, 0)
		}
	}

	// Dashed 7-day avg line
	dc.SetColor(textMain)
	dc.SetLineWidth(1.5)
	dc.SetDash(4, 3)
	for i, p := range maPts {
		if i == 0 {
			dc.MoveTo(p[0], p[1])
		} else {
			dc.LineTo(p[0], p[1])
		}
	}
	dc.Stroke()
	dc.SetDash()
}

func drawSpeedSparkline(dc *gg.Context, x, y, w, h float64, samples []model.ResourceSample, compact bool) {
	if len(samples) < 2 {
		drawSoftBox(dc, x, y, w, h, 8)
		setFont(dc, 12, false)
		dc.SetColor(textMuted)
		dc.DrawStringAnchored("暂无实时速率采样", x+w/2, y+h/2, 0.5, 0.5)
		return
	}
	maxBps := 1024.0
	for _, s := range samples {
		tot := s.RxBps + s.TxBps
		if tot > maxBps {
			maxBps = tot
		}
	}
	plotX := x
	plotW := w
	if !compact {
		plotX = x + 56
		plotW = w - 64
	}
	plotH := h - 18

	if !compact {
		setFont(dc, 10.5, false)
		for s := 0; s <= 3; s++ {
			gy := y + plotH - plotH*float64(s)/3.0
			dc.SetColor(color.RGBA{235, 231, 222, 255})
			dc.SetLineWidth(1)
			dc.DrawLine(plotX, gy, plotX+plotW, gy)
			dc.Stroke()
			dc.SetColor(textMuted)
			dc.DrawStringAnchored(formatBps(maxBps*float64(s)/3.0), plotX-6, gy+3, 1, 0)
		}
	}

	// Fill area
	dc.SetColor(colBlueSoft)
	dc.MoveTo(plotX, y+plotH)
	for i, s := range samples {
		px := plotX + plotW*float64(i)/float64(len(samples)-1)
		py := y + plotH - plotH*(s.RxBps+s.TxBps)/maxBps
		dc.LineTo(px, py)
	}
	dc.LineTo(plotX+plotW, y+plotH)
	dc.ClosePath()
	dc.Fill()

	// Line
	dc.SetColor(colBlue)
	dc.SetLineWidth(2)
	for i, s := range samples {
		px := plotX + plotW*float64(i)/float64(len(samples)-1)
		py := y + plotH - plotH*(s.RxBps+s.TxBps)/maxBps
		if i == 0 {
			dc.MoveTo(px, py)
		} else {
			dc.LineTo(px, py)
		}
	}
	dc.Stroke()
}

func drawResourceChart(dc *gg.Context, x, y, w, h float64, samples []model.ResourceSample) {
	plotX := x + 36
	plotW := w - 44
	plotH := h - 22

	setFont(dc, 10.5, false)
	for s := 0; s <= 4; s++ {
		gy := y + plotH - plotH*float64(s)/4.0
		dc.SetColor(color.RGBA{235, 231, 222, 255})
		dc.SetLineWidth(1)
		dc.DrawLine(plotX, gy, plotX+plotW, gy)
		dc.Stroke()
		dc.SetColor(textMuted)
		dc.DrawStringAnchored(fmt.Sprintf("%d%%", s*25), plotX-6, gy+3, 1, 0)
	}
	if len(samples) < 2 {
		return
	}

	drawSeries := func(col color.Color, pick func(model.ResourceSample) float64) {
		dc.SetColor(col)
		dc.SetLineWidth(2.2)
		for i, s := range samples {
			px := plotX + plotW*float64(i)/float64(len(samples)-1)
			v := pick(s)
			if v > 100 {
				v = 100
			}
			py := y + plotH - plotH*v/100.0
			if i == 0 {
				dc.MoveTo(px, py)
			} else {
				dc.LineTo(px, py)
			}
		}
		dc.Stroke()
	}

	drawSeries(colAmber, func(s model.ResourceSample) float64 { return s.CPUPct })
	drawSeries(colBlue, func(s model.ResourceSample) float64 { return s.MemPct })
	drawSeries(colGreen, func(s model.ResourceSample) float64 { return s.DiskPct })

	// Time labels
	for i := 0; i < len(samples); i += maxInt(1, len(samples)/5) {
		px := plotX + plotW*float64(i)/float64(len(samples)-1)
		if i == 0 {
			px += 12
		}
		dc.SetColor(textMuted)
		dc.DrawStringAnchored(toCST(samples[i].Timestamp).Format("15:04"), px, y+plotH+16, 0.5, 0)
	}
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func truncate(s string, maxRunes int) string {
	r := []rune(s)
	if len(r) <= maxRunes {
		return s
	}
	return string(r[:maxRunes-1]) + "…"
}
