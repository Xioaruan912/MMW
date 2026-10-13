package httpapi

import (
	"fmt"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/mmx-project/mmx/internal/notify"
	"github.com/mmx-project/mmx/internal/storage"
)

// ---------------------------------------------------------------------------
// 通知设置
// ---------------------------------------------------------------------------

func (s *Server) handleGetNotifySettings(w http.ResponseWriter, r *http.Request) {
	st, err := s.notifier.LoadSettings(r.Context())
	if err != nil {
		mapError(s.log, w, err, "notify settings get")
		return
	}
	// 敏感字段不回显，只告知是否已配置
	hasBotToken := st.Telegram.BotToken != ""
	hasWebhookSecret := st.Webhook.Secret != ""
	st.Telegram.BotToken = ""
	st.Webhook.Secret = ""
	writeJSON(w, http.StatusOK, map[string]any{
		"settings":           st,
		"has_telegram_token": hasBotToken,
		"has_webhook_secret": hasWebhookSecret,
		"events": []string{
			notify.EventServerOffline, notify.EventServerOnline,
			notify.EventUserCreated, notify.EventTrafficExceeded,
			notify.EventTaskFailed, notify.EventAgentTokenReuse,
		},
	})
}

// handlePutNotifySettings 保存通知设置。
//
// 空字符串的敏感字段表示「不修改」—— 面板不回显密钥，回填会把它清掉。
func (s *Server) handlePutNotifySettings(w http.ResponseWriter, r *http.Request) {
	cur, err := s.notifier.LoadSettings(r.Context())
	if err != nil {
		mapError(s.log, w, err, "notify settings load")
		return
	}
	var req notify.Settings
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", "请求体解析失败")
		return
	}
	if req.Telegram.BotToken == "" {
		req.Telegram.BotToken = cur.Telegram.BotToken
	}
	if req.Webhook.Secret == "" {
		req.Webhook.Secret = cur.Webhook.Secret
	}
	if err := s.notifier.SaveSettings(r.Context(), req); err != nil {
		mapError(s.log, w, err, "notify settings save")
		return
	}
	_, username, _ := UserFrom(r.Context())
	s.store.Audit(r.Context(), "notify.settings_update", username, "", "", s.clientIP(r))
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleTestNotify 发送一条测试通知。
func (s *Server) handleTestNotify(w http.ResponseWriter, r *http.Request) {
	if err := s.notifier.Test(r.Context()); err != nil {
		writeErr(w, http.StatusBadGateway, "notify_test_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "message": "测试通知已发送"})
}

// ---------------------------------------------------------------------------
// 公告
// ---------------------------------------------------------------------------

type announcementReq struct {
	Title     string `json:"title"`
	Content   string `json:"content"`
	Level     string `json:"level"`
	Enabled   *bool  `json:"enabled"`
	Pinned    *bool  `json:"pinned"`
	ExpiresAt int64  `json:"expires_at"`
}

// handleListAnnouncements 列出全部公告（管理端）。
func (s *Server) handleListAnnouncements(w http.ResponseWriter, r *http.Request) {
	list, err := s.store.ListAllAnnouncements(r.Context(), 100)
	if err != nil {
		mapError(s.log, w, err, "list announcements")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"announcements": list, "total": len(list)})
}

// handleCreateAnnouncement 新建公告。
func (s *Server) handleCreateAnnouncement(w http.ResponseWriter, r *http.Request) {
	var req announcementReq
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", "请求体解析失败")
		return
	}
	if req.Title == "" {
		writeErr(w, http.StatusBadRequest, "bad_request", "标题不能为空")
		return
	}
	enabled, pinned := true, false
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	if req.Pinned != nil {
		pinned = *req.Pinned
	}
	a, err := s.store.CreateAnnouncement(r.Context(), storage.Announcement{
		Title: req.Title, Content: req.Content, Level: req.Level,
		Enabled: enabled, Pinned: pinned, ExpiresAt: req.ExpiresAt,
	})
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	_, username, _ := UserFrom(r.Context())
	s.store.Audit(r.Context(), "announcement.create", username, a.ID, a.Title, s.clientIP(r))
	writeJSON(w, http.StatusCreated, map[string]any{"announcement": a})
}

// handleUpdateAnnouncement 更新公告。
func (s *Server) handleUpdateAnnouncement(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	cur, err := s.store.GetAnnouncement(r.Context(), id)
	if err != nil {
		mapError(s.log, w, err, "update announcement get")
		return
	}
	var req announcementReq
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", "请求体解析失败")
		return
	}
	if req.Title != "" {
		cur.Title = req.Title
	}
	cur.Content = req.Content
	if req.Level != "" {
		cur.Level = req.Level
	}
	if req.Enabled != nil {
		cur.Enabled = *req.Enabled
	}
	if req.Pinned != nil {
		cur.Pinned = *req.Pinned
	}
	cur.ExpiresAt = req.ExpiresAt
	if err := s.store.UpdateAnnouncement(r.Context(), cur); err != nil {
		mapError(s.log, w, err, "update announcement")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"announcement": cur})
}

// handleDeleteAnnouncement 删除公告。
func (s *Server) handleDeleteAnnouncement(w http.ResponseWriter, r *http.Request) {
	if err := s.store.DeleteAnnouncement(r.Context(), chi.URLParam(r, "id")); err != nil {
		mapError(s.log, w, err, "delete announcement")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleMyAnnouncements 返回当前用户可见的公告。
func (s *Server) handleMyAnnouncements(w http.ResponseWriter, r *http.Request) {
	list, err := s.store.ListActiveAnnouncements(r.Context(), 20)
	if err != nil {
		mapError(s.log, w, err, "my announcements")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"announcements": list, "total": len(list)})
}

// handlePublicAnnouncements 返回公开公告（登录页可使用）。
func (s *Server) handlePublicAnnouncements(w http.ResponseWriter, r *http.Request) {
	list, err := s.store.ListActiveAnnouncements(r.Context(), 10)
	if err != nil {
		mapError(s.log, w, err, "public announcements")
		return
	}
	// 公开接口只暴露展示所需字段
	out := make([]map[string]any, 0, len(list))
	for _, a := range list {
		out = append(out, map[string]any{
			"id": a.ID, "title": a.Title, "content": a.Content,
			"level": a.Level, "pinned": a.Pinned, "created_at": a.CreatedAt,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"announcements": out, "total": len(out)})
}

// ---------------------------------------------------------------------------
// 备份 / 恢复
// ---------------------------------------------------------------------------

// handleBackupDownload 下载数据库备份（zip 流式输出）。
func (s *Server) handleBackupDownload(w http.ResponseWriter, r *http.Request) {
	filename := fmt.Sprintf("mmx-backup-%s.zip", time.Now().Format("20060102-150405"))
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", `attachment; filename="`+filename+`"`)
	w.Header().Set("Cache-Control", "no-store")

	_, username, _ := UserFrom(r.Context())
	s.store.Audit(r.Context(), "backup.download", username, "", filename, s.clientIP(r))

	if err := s.backup.Create(r.Context(), w); err != nil {
		// 此时可能已经写了部分 zip 头，只能记日志
		s.log.Error("备份生成失败", "err", err)
	}
}

// handleBackupRestore 接收备份包并落成待恢复文件。
func (s *Server) handleBackupRestore(w http.ResponseWriter, r *http.Request) {
	_, username, _ := UserFrom(r.Context())
	manifest, err := s.backup.Restore(r.Body)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "restore_failed", err.Error())
		return
	}
	s.store.Audit(r.Context(), "backup.restore", username, "",
		fmt.Sprintf("version=%s created_at=%s", manifest.Version, manifest.CreatedAt.Format(time.RFC3339)),
		s.clientIP(r))
	writeJSON(w, http.StatusAccepted, map[string]any{
		"manifest":         manifest,
		"restart_required": true,
		"note":             "备份已落盘。请重启主控以应用；旧库会自动留档，可通过重命名回滚。",
	})
}
