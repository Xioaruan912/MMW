// Package httpapi 提供主控的 HTTP 接口与 Agent 网关。
package httpapi

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/mmx-project/mmx/internal/storage"
)

// APIError 是统一的错误响应体。
type APIError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if v == nil {
		return
	}
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
}

func writeErr(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, APIError{Code: code, Message: msg})
}

// mapError 把内部错误映射成合适的 HTTP 状态码。
func mapError(log *slog.Logger, w http.ResponseWriter, err error, fallback string) {
	switch {
	case errors.Is(err, storage.ErrNotFound):
		writeErr(w, http.StatusNotFound, "not_found", "资源不存在")
	case errors.Is(err, errBadRequest):
		writeErr(w, http.StatusBadRequest, "bad_request", err.Error())
	case errors.Is(err, errUnauthorized):
		writeErr(w, http.StatusUnauthorized, "unauthorized", "未授权")
	case errors.Is(err, errForbidden):
		writeErr(w, http.StatusForbidden, "forbidden", "无权限")
	default:
		log.Warn("请求处理失败", "fallback", fallback, "err", err)
		writeErr(w, http.StatusInternalServerError, "internal_error", err.Error())
	}
}

// decodeJSON 解析请求体，限制大小并禁止未知字段之外的宽松解析。
func decodeJSON(r *http.Request, dst any) error {
	if r.Body == nil {
		return errors.New("请求体为空")
	}
	dec := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 1<<20))
	if err := dec.Decode(dst); err != nil {
		return err
	}
	return nil
}

func findIn(r *http.Request, key string) string {
	v := r.URL.Query().Get(key)
	if v != "" {
		return v
	}
	return r.FormValue(key)
}
