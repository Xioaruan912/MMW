// Package notify 提供事件通知（Telegram / Webhook）。
//
// 设计要点：
//   - **所有通知都异步发送**，绝不阻塞主流程 —— 通知服务器挂了不该拖垮主控
//   - 同一个事件有去重窗口，避免服务器抖动时刷屏
//   - 发送失败只记日志，不重试（避免雪崩）；关键事件下一次状态变化时会再发
package notify

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/mmx-project/mmx/internal/storage"
)

// Event 是一次通知事件。
type Event struct {
	Kind      string    `json:"kind"` // server_offline / server_online / user_created / traffic_exceeded / task_failed
	Title     string    `json:"title"`
	Message   string    `json:"message"`
	Level     string    `json:"level"` // info / warning / error
	ServerID  string    `json:"server_id,omitempty"`
	UserID    string    `json:"user_id,omitempty"`
	Timestamp time.Time `json:"timestamp"`
}

// 事件类型。
const (
	EventServerOffline   = "server_offline"
	EventServerOnline    = "server_online"
	EventUserCreated     = "user_created"
	EventTrafficExceeded = "traffic_exceeded"
	EventTaskFailed      = "task_failed"
	EventAgentTokenReuse = "agent_token_reuse"
)

// TelegramSettings 是 Telegram 通知配置。
type TelegramSettings struct {
	Enabled  bool   `json:"enabled"`
	BotToken string `json:"bot_token"`
	ChatID   string `json:"chat_id"`
	// 只通知这些类型；为空表示全部
	Events []string `json:"events"`
}

// WebhookSettings 是 Webhook 通知配置。
type WebhookSettings struct {
	Enabled bool     `json:"enabled"`
	URL     string   `json:"url"`
	Secret  string   `json:"secret"` // 用于 HMAC-SHA256 签名（X-MMX-Signature）
	Events  []string `json:"events"`
}

// Settings 是通知总配置。
type Settings struct {
	Telegram TelegramSettings `json:"telegram"`
	Webhook  WebhookSettings  `json:"webhook"`
	// DedupWindowSeconds 是同类事件的去重窗口（默认 300 秒）
	DedupWindowSeconds int `json:"dedup_window_seconds"`
}

// SettingsKey 是通知配置在 system_settings 里的键名。
const SettingsKey = "notify.settings"

// DefaultSettings 返回默认配置（全部关闭）。
func DefaultSettings() Settings {
	return Settings{DedupWindowSeconds: 300}
}

// Notifier 负责读取配置并投递事件。
type Notifier struct {
	store *storage.Store
	log   *slog.Logger
	http  *http.Client

	mu   sync.Mutex
	seen map[string]time.Time // eventKey -> 上次发送时间
}

// New 构造 Notifier。
func New(store *storage.Store, log *slog.Logger) *Notifier {
	return &Notifier{
		store: store,
		log:   log,
		http:  &http.Client{Timeout: 15 * time.Second},
		seen:  map[string]time.Time{},
	}
}

// LoadSettings 读取配置。
func (n *Notifier) LoadSettings(ctx context.Context) (Settings, error) {
	st := DefaultSettings()
	if _, err := n.store.GetJSONSetting(ctx, SettingsKey, &st); err != nil {
		return st, err
	}
	if st.DedupWindowSeconds <= 0 {
		st.DedupWindowSeconds = 300
	}
	return st, nil
}

// SaveSettings 保存配置。
func (n *Notifier) SaveSettings(ctx context.Context, st Settings) error {
	if st.DedupWindowSeconds <= 0 {
		st.DedupWindowSeconds = 300
	}
	return n.store.SetJSONSetting(ctx, SettingsKey, st)
}

// Notify 异步投递事件。调用方不需要等待。
func (n *Notifier) Notify(ctx context.Context, ev Event) {
	if ev.Timestamp.IsZero() {
		ev.Timestamp = time.Now()
	}
	// 用后台 context，避免请求结束时把通知一起取消
	go func() {
		bg, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		n.deliver(bg, ev)
	}()
}

func (n *Notifier) deliver(ctx context.Context, ev Event) {
	st, err := n.LoadSettings(ctx)
	if err != nil {
		n.log.Warn("读取通知配置失败", "err", err)
		return
	}
	if n.isDuplicate(ev, st.DedupWindowSeconds) {
		n.log.Debug("通知被去重窗口抑制", "kind", ev.Kind)
		return
	}
	if st.Telegram.Enabled && subscribed(st.Telegram.Events, ev.Kind) {
		if err := n.sendTelegram(ctx, st.Telegram, ev); err != nil {
			n.log.Warn("Telegram 通知发送失败", "err", err)
		}
	}
	if st.Webhook.Enabled && subscribed(st.Webhook.Events, ev.Kind) {
		if err := n.sendWebhook(ctx, st.Webhook, ev); err != nil {
			n.log.Warn("Webhook 通知发送失败", "err", err)
		}
	}
}

// isDuplicate 判断是否在去重窗口内重复。
func (n *Notifier) isDuplicate(ev Event, windowSec int) bool {
	key := ev.Kind + "|" + ev.ServerID + "|" + ev.UserID
	now := time.Now()
	n.mu.Lock()
	defer n.mu.Unlock()
	// 顺带清理过期项，避免 map 无界增长
	for k, t := range n.seen {
		if now.Sub(t) > time.Duration(windowSec)*time.Second*4 {
			delete(n.seen, k)
		}
	}
	if last, ok := n.seen[key]; ok && now.Sub(last) < time.Duration(windowSec)*time.Second {
		return true
	}
	n.seen[key] = now
	return false
}

func subscribed(whitelist []string, kind string) bool {
	if len(whitelist) == 0 {
		return true
	}
	for _, k := range whitelist {
		if k == kind {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// Telegram
// ---------------------------------------------------------------------------

func (n *Notifier) sendTelegram(ctx context.Context, cfg TelegramSettings, ev Event) error {
	if cfg.BotToken == "" || cfg.ChatID == "" {
		return fmt.Errorf("Telegram 配置不完整（缺少 bot_token 或 chat_id）")
	}
	text := telegramText(ev)
	payload := map[string]any{
		"chat_id":                  cfg.ChatID,
		"text":                     text,
		"parse_mode":               "HTML",
		"disable_web_page_preview": true,
	}
	body, _ := json.Marshal(payload)
	url := "https://api.telegram.org/bot" + cfg.BotToken + "/sendMessage"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := n.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		buf := make([]byte, 512)
		nn, _ := resp.Body.Read(buf)
		return fmt.Errorf("Telegram API HTTP %d: %s", resp.StatusCode, string(buf[:nn]))
	}
	return nil
}

func telegramText(ev Event) string {
	icon := map[string]string{
		"info": "ℹ️", "warning": "⚠️", "error": "🚨",
	}[ev.Level]
	if icon == "" {
		icon = "ℹ️"
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "%s <b>%s</b>\n", icon, htmlEscape(ev.Title))
	if ev.Message != "" {
		sb.WriteString(htmlEscape(ev.Message) + "\n")
	}
	fmt.Fprintf(&sb, "<i>%s</i>", ev.Timestamp.Format("2006-01-02 15:04:05"))
	return sb.String()
}

func htmlEscape(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")
	return r.Replace(s)
}

// ---------------------------------------------------------------------------
// Webhook
// ---------------------------------------------------------------------------

func (n *Notifier) sendWebhook(ctx context.Context, cfg WebhookSettings, ev Event) error {
	if cfg.URL == "" {
		return fmt.Errorf("Webhook 未配置 URL")
	}
	body, err := json.Marshal(ev)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, cfg.URL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "mmx-notifier/1")
	req.Header.Set("X-MMX-Event", ev.Kind)
	if cfg.Secret != "" {
		mac := hmac.New(sha256.New, []byte(cfg.Secret))
		mac.Write(body)
		req.Header.Set("X-MMX-Signature", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	}
	resp, err := n.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("Webhook HTTP %d", resp.StatusCode)
	}
	return nil
}

// Test 发送一条测试通知，用于面板上的「测试」按钮。
func (n *Notifier) Test(ctx context.Context) error {
	st, err := n.LoadSettings(ctx)
	if err != nil {
		return err
	}
	ev := Event{
		Kind: "test", Title: "MMX 通知测试",
		Message: "如果你看到这条消息，说明通知通道已打通。",
		Level:   "info", Timestamp: time.Now(),
	}
	var errs []string
	if st.Telegram.Enabled {
		if err := n.sendTelegram(ctx, st.Telegram, ev); err != nil {
			errs = append(errs, "telegram: "+err.Error())
		}
	}
	if st.Webhook.Enabled {
		if err := n.sendWebhook(ctx, st.Webhook, ev); err != nil {
			errs = append(errs, "webhook: "+err.Error())
		}
	}
	if !st.Telegram.Enabled && !st.Webhook.Enabled {
		return fmt.Errorf("没有启用任何通知通道")
	}
	if len(errs) > 0 {
		return fmt.Errorf("部分通道失败: %s", strings.Join(errs, "; "))
	}
	return nil
}
