// Package logging 提供结构化日志与文件轮转。
package logging

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
)

// Options 控制日志输出行为。
type Options struct {
	Level    slog.Level
	Debug    bool
	LogDir   string // 为空则只写 stdout
	FileName string
	MaxBytes int64 // 单文件上限，超出后轮转
	MaxFiles int   // 保留的历史文件数
}

// Result 返回构造好的 logger 与需要关闭的资源。
type Result struct {
	Logger *slog.Logger
	closer func() error
}

// Close 关闭底层文件。
func (r *Result) Close() error {
	if r == nil || r.closer == nil {
		return nil
	}
	return r.closer()
}

// Setup 构建 logger。LogDir 为空时仅输出到 stderr。
func Setup(o Options) (*Result, error) {
	level := o.Level
	if o.Debug {
		level = slog.LevelDebug
	}

	var writers []io.Writer
	writers = append(writers, os.Stdout)

	var rot *rotator
	if o.LogDir != "" {
		if err := os.MkdirAll(o.LogDir, 0o755); err != nil {
			return nil, err
		}
		name := o.FileName
		if name == "" {
			name = "mmx.log"
		}
		r, err := newRotator(filepath.Join(o.LogDir, name), o.MaxBytes, o.MaxFiles)
		if err != nil {
			return nil, err
		}
		rot = r
		writers = append(writers, r)
	}

	out := io.MultiWriter(writers...)
	handler := slog.NewTextHandler(out, &slog.HandlerOptions{
		Level: level,
		ReplaceAttr: func(_ []string, a slog.Attr) slog.Attr {
			if a.Key == slog.TimeKey {
				a.Value = slog.StringValue(a.Value.Time().Format("2006-01-02 15:04:05"))
			}
			return a
		},
	})

	res := &Result{Logger: slog.New(handler)}
	if rot != nil {
		res.closer = rot.Close
	}
	return res, nil
}

// ---------------------------------------------------------------------------
// 极简大小轮转
// ---------------------------------------------------------------------------

type rotator struct {
	mu       sync.Mutex
	path     string
	maxBytes int64
	maxFiles int
	f        *os.File
	size     int64
}

func newRotator(path string, maxBytes int64, maxFiles int) (*rotator, error) {
	if maxBytes <= 0 {
		maxBytes = 32 << 20 // 32 MiB
	}
	if maxFiles <= 0 {
		maxFiles = 5
	}
	r := &rotator{path: path, maxBytes: maxBytes, maxFiles: maxFiles}
	if err := r.open(); err != nil {
		return nil, err
	}
	return r, nil
}

func (r *rotator) open() error {
	f, err := os.OpenFile(r.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	st, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return err
	}
	r.f = f
	r.size = st.Size()
	return nil
}

func (r *rotator) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.size+int64(len(p)) > r.maxBytes {
		r.rotate()
	}
	n, err := r.f.Write(p)
	r.size += int64(n)
	return n, err
}

func (r *rotator) rotate() {
	_ = r.f.Close()
	for i := r.maxFiles - 1; i >= 1; i-- {
		old := r.path + "." + itoa(i)
		newer := r.path + "." + itoa(i+1)
		if i == r.maxFiles-1 {
			_ = os.Remove(newer)
		}
		_ = os.Rename(old, newer)
	}
	_ = os.Rename(r.path, r.path+".1")
	_ = r.open()
}

func (r *rotator) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.f == nil {
		return nil
	}
	err := r.f.Close()
	r.f = nil
	return err
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
