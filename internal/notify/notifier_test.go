package notify

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/mmx-project/mmx/internal/storage"
)

func TestDefaultSettingsAllDisabled(t *testing.T) {
	st := DefaultSettings()
	if st.Telegram.Enabled || st.Webhook.Enabled {
		t.Fatal("通知通道必须默认关闭（避免用户不知情时把数据发到外部）")
	}
	if st.DedupWindowSeconds <= 0 {
		t.Fatal("去重窗口必须有默认值")
	}
}

func TestSubscribedWhitelist(t *testing.T) {
	if !subscribed(nil, "anything") {
		t.Fatal("空白名单表示全部订阅")
	}
	if !subscribed([]string{}, "anything") {
		t.Fatal("空列表表示全部订阅")
	}
	if !subscribed([]string{"a", "b"}, "b") {
		t.Fatal("命中白名单应当返回 true")
	}
	if subscribed([]string{"a", "b"}, "c") {
		t.Fatal("未命中白名单应当返回 false")
	}
}

func TestDedupWindowSuppressesRepeats(t *testing.T) {
	n := &Notifier{seen: map[string]time.Time{}}
	ev := Event{Kind: EventServerOffline, ServerID: "srv_1"}

	if n.isDuplicate(ev, 300) {
		t.Fatal("首次事件不应被去重")
	}
	if !n.isDuplicate(ev, 300) {
		t.Fatal("窗口内重复事件应被去重")
	}

	// 不同服务器不算重复
	ev2 := Event{Kind: EventServerOffline, ServerID: "srv_2"}
	if n.isDuplicate(ev2, 300) {
		t.Fatal("不同 server_id 不应互相抑制")
	}

	// 不同事件类型不算重复
	ev3 := Event{Kind: EventServerOnline, ServerID: "srv_1"}
	if n.isDuplicate(ev3, 300) {
		t.Fatal("不同 kind 不应互相抑制")
	}
}

func TestDedupWindowExpires(t *testing.T) {
	n := &Notifier{seen: map[string]time.Time{}}
	ev := Event{Kind: EventUserCreated, UserID: "usr_1"}
	if n.isDuplicate(ev, 1) {
		t.Fatal("首次不应去重")
	}
	// 手工把上次时间推到窗口之外
	n.mu.Lock()
	n.seen[ev.Kind+"|"+ev.ServerID+"|"+ev.UserID] = time.Now().Add(-2 * time.Second)
	n.mu.Unlock()

	if n.isDuplicate(ev, 1) {
		t.Fatal("超出窗口后不应再去重")
	}
}

func TestDedupMapDoesNotGrowUnbounded(t *testing.T) {
	n := &Notifier{seen: map[string]time.Time{}}
	// 灌入大量早已过期的事件
	old := time.Now().Add(-24 * time.Hour)
	for i := 0; i < 500; i++ {
		n.seen[string(rune('a'+i%26))+string(rune('0'+i%10))+string(rune(i))] = old
	}
	_ = n.isDuplicate(Event{Kind: "x", ServerID: "y"}, 60)
	n.mu.Lock()
	size := len(n.seen)
	n.mu.Unlock()
	if size > 50 {
		t.Fatalf("过期条目应被清理，当前 %d 条", size)
	}
}

func TestTelegramTextEscapesHTML(t *testing.T) {
	text := telegramText(Event{
		Title:   "<b>注入</b>",
		Message: "a & b > c",
		Level:   "error",
	})
	if contains(text, "<b>注入</b>") {
		t.Fatalf("标题中的 HTML 必须被转义: %s", text)
	}
	if !contains(text, "&lt;b&gt;") {
		t.Fatalf("转义结果不正确: %s", text)
	}
	if !contains(text, "&amp;") {
		t.Fatalf("& 应当被转义: %s", text)
	}
	// 我们自己的标签要保留
	if !contains(text, "<i>") {
		t.Fatalf("时间戳斜体标签应保留: %s", text)
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (func() bool {
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	})()
}

// newTestNotifier 建一个带真实存储的 Notifier。
// 用真库而不是 mock：设置项的序列化/反序列化路径本身也需要覆盖。
func newTestNotifier(t *testing.T) (*Notifier, *storage.Store) {
	t.Helper()
	db, err := storage.Open("sqlite", filepath.Join(t.TempDir(), "n.db"))
	if err != nil {
		t.Fatalf("打开测试库失败: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	st, err := storage.New(context.Background(), db)
	if err != nil {
		t.Fatalf("迁移失败: %v", err)
	}
	return New(st, slog.New(slog.NewTextHandler(io.Discard, nil))), st
}

func TestSettingsRoundTrip(t *testing.T) {
	n, _ := newTestNotifier(t)
	ctx := context.Background()

	// 未配置时读到默认值
	st, err := n.LoadSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if st.Telegram.Enabled || st.Webhook.Enabled {
		t.Fatal("默认应为全关闭")
	}
	if st.DedupWindowSeconds <= 0 {
		t.Fatal("默认去重窗口未生效")
	}

	// 保存后能读回
	in := Settings{
		Telegram: TelegramSettings{Enabled: true, BotToken: "tok", ChatID: "123", Events: []string{EventServerOffline}},
		Webhook:  WebhookSettings{Enabled: true, URL: "https://example.com/hook", Secret: "s3cr3t"},
	}
	if err := n.SaveSettings(ctx, in); err != nil {
		t.Fatal(err)
	}
	out, err := n.LoadSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !out.Telegram.Enabled || out.Telegram.BotToken != "tok" || out.Telegram.ChatID != "123" {
		t.Fatalf("Telegram 配置未正确持久化: %+v", out.Telegram)
	}
	if !out.Webhook.Enabled || out.Webhook.URL != "https://example.com/hook" || out.Webhook.Secret != "s3cr3t" {
		t.Fatalf("Webhook 配置未正确持久化: %+v", out.Webhook)
	}
	if len(out.Telegram.Events) != 1 || out.Telegram.Events[0] != EventServerOffline {
		t.Fatalf("事件白名单未持久化: %+v", out.Telegram.Events)
	}
	// 非法去重窗口应回落默认值
	if err := n.SaveSettings(ctx, Settings{DedupWindowSeconds: -1}); err != nil {
		t.Fatal(err)
	}
	out2, _ := n.LoadSettings(ctx)
	if out2.DedupWindowSeconds <= 0 {
		t.Fatal("非法去重窗口应回落默认值")
	}
}

func TestTestRequiresChannel(t *testing.T) {
	// 未配置任何通道时，Test 应明确报错而不是静默成功
	n, _ := newTestNotifier(t)
	if err := n.Test(context.Background()); err == nil {
		t.Fatal("未启用通道时应报错")
	}
}
