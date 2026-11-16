package bot

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"netwatch/internal/config"
	"netwatch/internal/probe"
	"netwatch/internal/render"
	"netwatch/internal/service"
	"netwatch/internal/store"
)

type Bot struct {
	cfg     *config.Config
	agg     *service.Aggregator
	store   *store.Store
	client  *http.Client
	allowed map[int64]bool
}

func New(cfg *config.Config, agg *service.Aggregator, st *store.Store) *Bot {
	allowed := make(map[int64]bool, len(cfg.Telegram.AllowedUserIDs))
	for _, id := range cfg.Telegram.AllowedUserIDs {
		allowed[id] = true
	}
	return &Bot{
		cfg:     cfg,
		agg:     agg,
		store:   st,
		client:  &http.Client{Timeout: 45 * time.Second},
		allowed: allowed,
	}
}

type tgUpdateResp struct {
	OK     bool       `json:"ok"`
	Result []tgUpdate `json:"result"`
}

type tgUpdate struct {
	UpdateID      int64            `json:"update_id"`
	Message       *tgMessage       `json:"message"`
	CallbackQuery *tgCallbackQuery `json:"callback_query"`
}

type tgMessage struct {
	MessageID int64  `json:"message_id"`
	Text      string `json:"text"`
	Chat      struct {
		ID int64 `json:"id"`
	} `json:"chat"`
	From *struct {
		ID int64 `json:"id"`
	} `json:"from"`
}

type tgCallbackQuery struct {
	ID      string     `json:"id"`
	Data    string     `json:"data"`
	Message *tgMessage `json:"message"`
	From    struct {
		ID int64 `json:"id"`
	} `json:"from"`
}

type inlineBtn struct {
	Text         string `json:"text"`
	CallbackData string `json:"callback_data"`
}

func (b *Bot) Run(ctx context.Context) {
	if b.cfg.Telegram.BotToken == "" {
		log.Println("telegram bot_token is empty; bot polling disabled")
		<-ctx.Done()
		return
	}
	b.registerMenu()
	var offset int64
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		updates, err := b.getUpdates(ctx, offset)
		if err != nil {
			log.Printf("tg getUpdates err: %v", err)
			time.Sleep(3 * time.Second)
			continue
		}
		for _, u := range updates {
			if u.UpdateID >= offset {
				offset = u.UpdateID + 1
			}
			if u.Message != nil {
				b.handleMessage(ctx, u.Message)
			}
			if u.CallbackQuery != nil {
				b.handleCallback(ctx, u.CallbackQuery)
			}
		}
	}
}

func (b *Bot) isAllowed(userID int64) bool {
	if len(b.allowed) == 0 {
		return true
	}
	return b.allowed[userID]
}

func (b *Bot) registerMenu() {
	b.postJSON("setMyCommands", map[string]any{
		"commands": []map[string]string{
			{"command": "status", "description": "📊 节点总览"},
			{"command": "clients", "description": "🌐 在线 IP 与流量消耗"},
			{"command": "gd", "description": "📡 测广东三网延迟"},
			{"command": "chart", "description": "🖼 发送总览卡片图"},
		},
	})
}

func (b *Bot) handleMessage(ctx context.Context, msg *tgMessage) {
	if msg.From == nil || !b.isAllowed(msg.From.ID) {
		return
	}
	rawText := strings.TrimSpace(msg.Text)
	if rawText == "" {
		return
	}

	cmd := strings.Fields(rawText)
	c := strings.Split(cmd[0], "@")[0]
	switch c {
	case "/start":
		// Explicitly remove any lingering reply keyboard on the user's Telegram client.
		b.postJSON("sendMessage", map[string]any{
			"chat_id":      msg.Chat.ID,
			"text":         "已收起底部键盘，使用左下角菜单或消息按钮即可。",
			"reply_markup": map[string]any{"remove_keyboard": true},
		})
		b.sendOverview(ctx, msg.Chat.ID, 0)
	case "/status", "/watch", "/text":
		b.sendOverview(ctx, msg.Chat.ID, 0)
	case "/chart":
		b.sendOverviewPhoto(ctx, msg.Chat.ID)
	case "/gd":
		b.sendText(msg.Chat.ID, "正在用 Globalping 广州/深圳探针实测节点延迟，约 5 秒...")
		b.runGuangdongProbes(ctx)
		b.sendOverview(ctx, msg.Chat.ID, 0)
	case "/clients":
		list, err := b.agg.AllClientsEnriched(30)
		if err != nil {
			b.sendText(msg.Chat.ID, "查询失败: "+err.Error())
			return
		}
		b.sendText(msg.Chat.ID, render.RenderClientsOnlyText(list))
	case "/set":
		if len(cmd) < 3 {
			b.sendText(msg.Chat.ID, "用法：<code>/set vps-154 320.4</code>（校准本周期已用 GB）")
			return
		}
		nodeID := cmd[1]
		for _, nd := range b.agg.ActiveNodes() {
			if nd.ID == nodeID || strings.Contains(nd.ID, nodeID) || strings.Contains(nd.Name, nodeID) {
				nodeID = nd.ID
				break
			}
		}
		gbVal, err := strconv.ParseFloat(cmd[2], 64)
		if err != nil || gbVal < 0 {
			b.sendText(msg.Chat.ID, "流量数值格式不对，例如：<code>/set vps-154 320.4</code>")
			return
		}
		if err := b.agg.CalibrateNodeMonthGB(nodeID, gbVal); err != nil {
			b.sendText(msg.Chat.ID, "校准失败: "+err.Error())
			return
		}
		b.sendText(msg.Chat.ID, fmt.Sprintf("已校准 <code>%s</code> 本周期已用流量为 <b>%.2f GB</b>", nodeID, gbVal))
		b.sendOverview(ctx, msg.Chat.ID, 0)
	default:
		b.sendOverview(ctx, msg.Chat.ID, 0)
	}
}

func (b *Bot) handleCallback(ctx context.Context, cb *tgCallbackQuery) {
	if !b.isAllowed(cb.From.ID) || cb.Message == nil {
		b.answerCallback(cb.ID, "无权限")
		return
	}
	parts := strings.Split(cb.Data, ":")
	chatID := cb.Message.Chat.ID
	msgID := cb.Message.MessageID

	switch parts[0] {
	case "ov":
		b.answerCallback(cb.ID, "已刷新")
		b.sendOverview(ctx, chatID, msgID)
	case "nd":
		if len(parts) < 2 {
			return
		}
		b.answerCallback(cb.ID, "已切换节点")
		b.sendNodeDetail(ctx, chatID, msgID, parts[1])
	case "clients":
		b.answerCallback(cb.ID, "已刷新在线 IP")
		list, err := b.agg.AllClientsEnriched(30)
		if err != nil {
			b.sendText(chatID, "查询失败: "+err.Error())
			return
		}
		kb := [][]inlineBtn{{
			{Text: "🔄 刷新", CallbackData: "clients"},
			{Text: "⬅️ 返回总览", CallbackData: "ov"},
		}}
		b.editText(chatID, msgID, render.RenderClientsOnlyText(list), kb)
	case "gd":
		b.answerCallback(cb.ID, "正在测广东三网延迟...")
		b.runGuangdongProbes(ctx)
		if len(parts) > 1 && parts[1] != "" {
			b.sendNodeDetail(ctx, chatID, msgID, parts[1])
		} else {
			b.sendOverview(ctx, chatID, msgID)
		}
	}
}

func (b *Bot) sendOverview(ctx context.Context, chatID, editMsgID int64) {
	v, err := b.agg.BuildOverview("month")
	if err != nil {
		b.sendText(chatID, "生成总览失败: "+err.Error())
		return
	}
	kb := b.overviewKeyboard()
	txt := render.RenderOverviewText(v)
	if editMsgID > 0 {
		b.editText(chatID, editMsgID, txt, kb)
	} else {
		b.sendTextWithKB(chatID, txt, kb)
	}
}

func (b *Bot) sendOverviewPhoto(ctx context.Context, chatID int64) {
	v, err := b.agg.BuildOverview("month")
	if err != nil {
		b.sendText(chatID, "生成卡片失败: "+err.Error())
		return
	}
	tmp := filepath.Join(os.TempDir(), fmt.Sprintf("netwatch_ov_%d.png", time.Now().UnixNano()))
	defer os.Remove(tmp)
	if err := render.RenderOverviewPNG(v, tmp); err != nil {
		b.sendText(chatID, render.RenderOverviewText(v))
		return
	}
	caption := fmt.Sprintf("📊 <b>%s</b> · 在线节点 %d · 实时总速 %.1f KB/s",
		v.Title, len(v.Nodes), v.CurrentBps/1024)
	b.sendPhoto(chatID, tmp, caption, nil)
}

func (b *Bot) sendNodeDetail(ctx context.Context, chatID, editMsgID int64, nodeID string) {
	v, err := b.agg.BuildNodeDetail(nodeID, "24h")
	if err != nil {
		b.sendText(chatID, "生成详情失败: "+err.Error())
		return
	}
	kb := b.detailKeyboard(nodeID)
	txt := render.RenderNodeDetailText(v)
	if editMsgID > 0 {
		b.editText(chatID, editMsgID, txt, kb)
	} else {
		b.sendTextWithKB(chatID, txt, kb)
	}
}

func (b *Bot) runGuangdongProbes(ctx context.Context) {
	for _, nd := range b.agg.ActiveNodes() {
		if nd.Host == "" {
			continue
		}
		probes, err := probe.MeasureGuangdong(ctx, nd.Host, nd.Port, nd.ProbePort, nd.Protocol, b.cfg.GlobalpingToken)
		if err != nil {
			log.Printf("globalping probe %s err: %v", nd.ID, err)
			continue
		}
		if len(probes) > 0 {
			_ = b.store.SaveProbes(nd.ID, probes)
		}
	}
}

func (b *Bot) overviewKeyboard() [][]inlineBtn {
	rows := [][]inlineBtn{
		{
			{Text: "🔄 刷新", CallbackData: "ov"},
			{Text: "📡 测广东", CallbackData: "gd:"},
			{Text: "🌐 在线 IP", CallbackData: "clients"},
		},
	}
	var nodeRow []inlineBtn
	for _, nd := range b.agg.ActiveNodes() {
		nodeRow = append(nodeRow, inlineBtn{
			Text:         nd.Name,
			CallbackData: "nd:" + nd.ID,
		})
		if len(nodeRow) == 2 {
			rows = append(rows, nodeRow)
			nodeRow = nil
		}
	}
	if len(nodeRow) > 0 {
		rows = append(rows, nodeRow)
	}
	return rows
}

func (b *Bot) detailKeyboard(nodeID string) [][]inlineBtn {
	return [][]inlineBtn{
		{
			{Text: "🔄 刷新", CallbackData: "nd:" + nodeID},
			{Text: "📡 测广东", CallbackData: "gd:" + nodeID},
			{Text: "⬅️ 返回", CallbackData: "ov"},
		},
	}
}

func (b *Bot) apiURL(method string) string {
	return fmt.Sprintf("https://api.telegram.org/bot%s/%s", b.cfg.Telegram.BotToken, method)
}

func (b *Bot) getUpdates(ctx context.Context, offset int64) ([]tgUpdate, error) {
	url := fmt.Sprintf("%s?timeout=30&offset=%d", b.apiURL("getUpdates"), offset)
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	resp, err := b.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var r tgUpdateResp
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return nil, err
	}
	return r.Result, nil
}

func (b *Bot) answerCallback(id, text string) {
	b.postJSON("answerCallbackQuery", map[string]any{
		"callback_query_id": id,
		"text":              text,
	})
}

func (b *Bot) sendText(chatID int64, text string) {
	b.postJSON("sendMessage", map[string]any{
		"chat_id":    chatID,
		"text":       text,
		"parse_mode": "HTML",
	})
}

func (b *Bot) sendTextWithKB(chatID int64, text string, kb [][]inlineBtn) {
	b.postJSON("sendMessage", map[string]any{
		"chat_id":      chatID,
		"text":         text,
		"parse_mode":   "HTML",
		"reply_markup": map[string]any{"inline_keyboard": kb},
	})
}

func (b *Bot) editText(chatID, msgID int64, text string, kb [][]inlineBtn) {
	b.postJSON("editMessageText", map[string]any{
		"chat_id":      chatID,
		"message_id":   msgID,
		"text":         text,
		"parse_mode":   "HTML",
		"reply_markup": map[string]any{"inline_keyboard": kb},
	})
}

func (b *Bot) sendPhoto(chatID int64, path, caption string, kb [][]inlineBtn) {
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	_ = mw.WriteField("chat_id", fmt.Sprintf("%d", chatID))
	_ = mw.WriteField("caption", caption)
	_ = mw.WriteField("parse_mode", "HTML")
	if len(kb) > 0 {
		rm, _ := json.Marshal(map[string]any{"inline_keyboard": kb})
		_ = mw.WriteField("reply_markup", string(rm))
	}
	fw, err := mw.CreateFormFile("photo", filepath.Base(path))
	if err != nil {
		return
	}
	f, err := os.Open(path)
	if err != nil {
		return
	}
	_, _ = io.Copy(fw, f)
	f.Close()
	mw.Close()

	req, _ := http.NewRequest(http.MethodPost, b.apiURL("sendPhoto"), &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	resp, err := b.client.Do(req)
	if err == nil {
		resp.Body.Close()
	}
}

func (b *Bot) postJSON(method string, payload any) {
	raw, _ := json.Marshal(payload)
	resp, err := b.client.Post(b.apiURL(method), "application/json", bytes.NewReader(raw))
	if err == nil {
		resp.Body.Close()
	}
}
