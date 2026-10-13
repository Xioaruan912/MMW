package httpapi

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/mmx-project/mmx/internal/agentlink"
	"github.com/mmx-project/mmx/internal/storage"
	"github.com/mmx-project/mmx/pkg/agentproto"
)

// ---------------------------------------------------------------------------
// enroll：一次性令牌换长期令牌
// ---------------------------------------------------------------------------

type enrollReq struct {
	EnrollToken string                  `json:"enroll_token"`
	Hello       agentproto.HelloPayload `json:"hello"`
}

type enrollResp struct {
	ServerID           string `json:"server_id"`
	ServerName         string `json:"server_name"`
	AgentToken         string `json:"agent_token"`
	MasterPublicKey    string `json:"master_public_key"`
	ProtocolVersion    int    `json:"protocol_version"`
	HeartbeatIntervalS int    `json:"heartbeat_interval_sec"`
	MetricsIntervalS   int    `json:"metrics_interval_sec"`
	ServerTime         int64  `json:"server_time"`
}

// handleAgentEnroll 处理 Agent 首次入驻。
func (s *Server) handleAgentEnroll(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	ip := s.clientIP(r)

	var req enrollReq
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", "请求体解析失败")
		return
	}
	if req.EnrollToken == "" {
		writeErr(w, http.StatusBadRequest, "bad_request", "缺少 enroll_token")
		return
	}

	tok, err := s.reg.ConsumeEnrollToken(ctx, req.EnrollToken, ip)
	if err != nil {
		writeErr(w, http.StatusUnauthorized, agentproto.CodeUnauthorized, err.Error())
		return
	}

	plain, _, err := s.reg.IssueAgentToken(ctx, tok.ServerID, "enroll")
	if err != nil {
		mapError(s.log, w, err, "enroll issue agent token")
		return
	}
	sv, err := s.store.GetServer(ctx, tok.ServerID)
	if err != nil {
		mapError(s.log, w, err, "enroll get server")
		return
	}
	_ = s.store.MarkServerSeen(ctx, sv.ID, ip, req.Hello.AgentVersion, agentproto.Version, "")

	s.log.Info("agent 完成入驻", "server", sv.ID, "name", sv.Name, "ip", ip,
		"host", req.Hello.Hostname, "agent", req.Hello.AgentVersion)

	ack := s.reg.HelloAck(sv)
	writeJSON(w, http.StatusOK, enrollResp{
		ServerID:           sv.ID,
		ServerName:         sv.Name,
		AgentToken:         plain,
		MasterPublicKey:    ack.MasterPublicKey,
		ProtocolVersion:    agentproto.Version,
		HeartbeatIntervalS: ack.HeartbeatIntervalSec,
		MetricsIntervalS:   ack.MetricsIntervalSec,
		ServerTime:         time.Now().UnixMilli(),
	})
}

// ---------------------------------------------------------------------------
// pull：WS 不可用时的降级通道
// ---------------------------------------------------------------------------

type pullReq struct {
	Hello     *agentproto.HelloPayload     `json:"hello,omitempty"`
	Heartbeat *agentproto.HeartbeatPayload `json:"heartbeat,omitempty"`
	Metrics   *agentproto.MetricsPayload   `json:"metrics,omitempty"`
	// 上一轮取走的任务的执行结果（Pull 模式下没有反向通道，只能随上报带回来）
	Results []agentproto.TaskResultPayload `json:"results,omitempty"`
}

type pullResp struct {
	Tasks      []agentproto.TaskPayload  `json:"tasks"`
	Config     *agentproto.ConfigPayload `json:"config,omitempty"`
	ServerTime int64                     `json:"server_time"`
}

// pullTaskTTL 是任务在队列里等待 Agent 拉取的最长时间。
// 超过这个时间仍未取走的任务视为陈旧，不再下发（避免 Agent 长时间离线后
// 一次性收到大量早已失去意义的指令，例如重启早已不存在进程）。
const pullTaskTTL = 15 * time.Minute

// handleAgentPull 是 WS 的降级通道：上报 + 取任务，一次往返。
//
// 与 WS 模式的**能力对齐**：任务从同一张 pending_tasks 表取，
// 同样带签名与 nonce，Agent 侧走完全一致的验签与白名单校验。
func (s *Server) handleAgentPull(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	plain := agentlink.TokenFromRequest(r)

	sv, _, err := s.reg.AuthenticateAgentToken(ctx, plain)
	if err != nil {
		writeErr(w, http.StatusUnauthorized, agentproto.CodeUnauthorized, err.Error())
		return
	}

	var req pullReq
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", "请求体解析失败")
		return
	}

	ip := s.clientIP(r)
	agentVersion := ""
	if req.Hello != nil {
		agentVersion = req.Hello.AgentVersion
	}
	_ = s.store.MarkServerSeen(ctx, sv.ID, ip, agentVersion, agentproto.Version, "")

	// 1) 先收取上一轮任务的结果
	for _, res := range req.Results {
		status := storage.TaskFailed
		switch res.Status {
		case agentproto.TaskStatusOK:
			status = storage.TaskOK
		case agentproto.TaskStatusRejected:
			status = storage.TaskCancelled
		}
		_ = s.store.FinishTask(ctx, res.TaskID, status, res.Output, res.Error)
		s.log.Info("任务完成（Pull）", "server", sv.ID, "task", res.TaskID,
			"status", res.Status, "err", res.Error)
		// 同时喂给可能仍在等待中的同步调用方（例如管理面板刚发起就断了 WS）
		s.hub.DeliverResult(res)
	}

	// 2) 落指标
	if req.Metrics != nil {
		m := storage.ServerMetrics{
			ServerID: sv.ID, TS: req.Metrics.TS,
			CPUPercent: req.Metrics.CPUPercent,
			MemUsed:    req.Metrics.MemUsed, MemTotal: req.Metrics.MemTotal,
			DiskUsed: req.Metrics.DiskUsed, DiskTotal: req.Metrics.DiskTotal,
			Load1: req.Metrics.Load1, Load5: req.Metrics.Load5, Load15: req.Metrics.Load15,
			NetUp: req.Metrics.NetUp, NetDown: req.Metrics.NetDown,
			TCPConns: req.Metrics.TCPConns, UDPConns: req.Metrics.UDPConns,
			Processes: req.Metrics.Processes, UptimeSec: req.Metrics.UptimeSec,
			XrayRunning: req.Metrics.XrayRunning,
		}
		if req.Metrics.TS == 0 {
			m.TS = time.Now().UnixMilli()
		}
		_ = s.store.InsertMetrics(ctx, m)
	}

	// 3) 取走待投递任务（已标记为 dispatched，不会重复下发）
	claimed, err := s.store.ClaimPendingTasks(ctx, sv.ID, 10, pullTaskTTL)
	if err != nil {
		mapError(s.log, w, err, "agent pull claim")
		return
	}
	tasks := make([]agentproto.TaskPayload, 0, len(claimed))
	for _, t := range claimed {
		var args agentproto.RawArgs
		if t.Args != "" {
			_ = json.Unmarshal([]byte(t.Args), &args)
		}
		tasks = append(tasks, agentproto.TaskPayload{
			TaskID: t.ID, Kind: t.Kind, Nonce: t.Nonce, Signature: t.Signature,
			DeadlineMS: t.DeadlineMS, Args: args,
		})
	}

	writeJSON(w, http.StatusOK, pullResp{Tasks: tasks, ServerTime: time.Now().UnixMilli()})
}

// ---------------------------------------------------------------------------
// 安装脚本与二进制
// ---------------------------------------------------------------------------

// handleInstallScript 生成 Agent 一键安装脚本。
// 这里**不消费** enroll token —— token 由 Agent 运行时通过 /api/agent/enroll 消费。
func (s *Server) handleInstallScript(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	plain := r.URL.Query().Get("token")
	if plain == "" {
		writeErr(w, http.StatusBadRequest, "bad_request", "缺少 token 参数")
		return
	}
	xrayMode := r.URL.Query().Get("xray_mode")
	if xrayMode == "" {
		xrayMode = storage.XrayModeExternal
	}
	switch xrayMode {
	case storage.XrayModeEmbedded, storage.XrayModeExternal, storage.XrayModeNone:
	default:
		writeErr(w, http.StatusBadRequest, "bad_request", "xray_mode 非法")
		return
	}

	// 校验令牌存在且尚未消费，但不标记为已用
	tok, err := s.store.FindTokenByHash(ctx, hashToken(plain))
	if err != nil {
		writeErr(w, http.StatusNotFound, "not_found", "入驻令牌不存在")
		return
	}
	if tok.Kind != storage.TokenKindEnroll || tok.RevokedAt != 0 || tok.UsedAt != 0 {
		writeErr(w, http.StatusForbidden, "token_invalid", "入驻令牌无效或已被使用")
		return
	}
	if tok.ExpiresAt != 0 && time.Now().UnixMilli() > tok.ExpiresAt {
		writeErr(w, http.StatusForbidden, "token_expired", "入驻令牌已过期")
		return
	}

	inst := agentlink.Installer{
		BaseURL:  s.cfg.BaseURL(),
		Token:    plain,
		ServerID: tok.ServerID,
		XrayMode: xrayMode,
		Binaries: s.agentBinaryHashes(),
	}
	script := inst.Script()

	w.Header().Set("Content-Type", "text/x-shellscript; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(script))
}

// handleAgentBinary 提供 Agent 二进制下载。
func (s *Server) handleAgentBinary(w http.ResponseWriter, r *http.Request) {
	if s.agentBinaryDir == "" {
		writeErr(w, http.StatusNotFound, "not_found",
			"主控未配置 Agent 二进制库（设置 MMX_AGENT_BINARY_DIR）")
		return
	}
	platform := chi.URLParam(r, "platform")
	name := "mmx-agent-" + filepath.Base(platform)
	p := filepath.Join(s.agentBinaryDir, name)
	st, err := os.Stat(p)
	if err != nil || st.IsDir() {
		writeErr(w, http.StatusNotFound, "not_found", "该平台的 Agent 二进制不存在: "+name)
		return
	}
	f, err := os.Open(p)
	if err != nil {
		mapError(s.log, w, err, "open agent binary")
		return
	}
	defer f.Close()
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", itoa64(st.Size()))
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	_, _ = io.Copy(w, f)
}

// agentBinaryHashes 扫描二进制库，产出 "linux-amd64" -> sha256。
func (s *Server) agentBinaryHashes() map[string]string {
	out := map[string]string{}
	if s.agentBinaryDir == "" {
		return out
	}
	entries, err := os.ReadDir(s.agentBinaryDir)
	if err != nil {
		return out
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		const prefix = "mmx-agent-"
		if !strings.HasPrefix(name, prefix) {
			continue
		}
		platform := strings.TrimPrefix(name, prefix)
		sum, err := fileSHA256(filepath.Join(s.agentBinaryDir, name))
		if err == nil {
			out[platform] = sum
		}
	}
	return out
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// ---------------------------------------------------------------------------
// 前端静态资源
// ---------------------------------------------------------------------------

// serveSPA 从内嵌文件系统提供前端；未命中的路径回落到 index.html（单页应用路由）。
func (s *Server) serveSPA(w http.ResponseWriter, r *http.Request) {
	if s.webFS == nil {
		writeErr(w, http.StatusNotFound, "not_found", "前端未构建")
		return
	}
	upath := strings.TrimPrefix(r.URL.Path, "/")
	if upath == "" {
		upath = "index.html"
	}
	if f, err := s.webFS.Open(upath); err == nil {
		defer f.Close()
		if st, err := f.Stat(); err == nil && !st.IsDir() {
			if strings.HasPrefix(upath, "assets/") {
				w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
			}
			serveFile(w, r, upath, st.ModTime(), f)
			return
		}
	}
	// SPA 回落
	idx, err := fs.ReadFile(s.webFS, "index.html")
	if err != nil {
		writeErr(w, http.StatusNotFound, "not_found", "未找到")
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(idx)
}

// serveFile 提供静态文件。
//
// 不使用无保护的 `f.(io.ReadSeeker)` —— 一旦底层 fs.File 不实现 ReadSeeker，
// 类型断言会 panic（embed.FS 的实现细节不应成为线上风险）。
// 支持 Seek 时走 ServeContent（获得 Range / If-Modified-Since 能力），
// 否则退化为整体拷贝。
func serveFile(w http.ResponseWriter, r *http.Request, name string, modTime time.Time, f fs.File) {
	if rs, ok := f.(io.ReadSeeker); ok {
		http.ServeContent(w, r, name, modTime, rs)
		return
	}
	if st, err := f.Stat(); err == nil {
		if ct := mime.TypeByExtension(filepath.Ext(name)); ct != "" {
			w.Header().Set("Content-Type", ct)
		}
		w.Header().Set("Last-Modified", modTime.UTC().Format(http.TimeFormat))
		w.Header().Set("Content-Length", itoa64(st.Size()))
	}
	w.WriteHeader(http.StatusOK)
	_, _ = io.Copy(w, f)
}

func hashToken(t string) string {
	h := sha256.Sum256([]byte(t))
	return hex.EncodeToString(h[:])
}

func itoa64(n int64) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [24]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}
