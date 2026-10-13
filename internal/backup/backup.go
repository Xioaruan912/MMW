// Package backup 提供数据库备份与恢复。
//
// 一致性：备份用 SQLite 的 `VACUUM INTO` 生成快照，而不是直接拷贝 .db 文件。
// 直接拷贝在 WAL 模式下可能拿到「主库已更新、WAL 未合并」的不一致副本。
//
// 恢复：**不在运行时原地替换数据库**。恢复请求只把上传的备份落到
// `<data>/mmx.db.restore-pending`，由下次启动时原子替换并留档旧库。
// 运行中替换会同时踩到：连接池持有的句柄、WAL 文件、以及一半内存状态没刷新。
package backup

import (
	"archive/zip"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/mmx-project/mmx/internal/version"
)

// Manifest 是备份包的元信息。
type Manifest struct {
	App       string           `json:"app"`
	Version   string           `json:"version"`
	CreatedAt time.Time        `json:"created_at"`
	Driver    string           `json:"driver"`
	Tables    map[string]int64 `json:"tables"`
	Note      string           `json:"note,omitempty"`
}

// Manager 负责备份与恢复。
type Manager struct {
	db      *sql.DB
	dataDir string
	driver  string
}

// NewManager 构造 Manager。
func NewManager(db *sql.DB, dataDir, driver string) *Manager {
	return &Manager{db: db, dataDir: dataDir, driver: driver}
}

// PendingRestorePath 返回待应用的恢复文件路径。
func (m *Manager) PendingRestorePath() string {
	return filepath.Join(m.dataDir, "mmx.db.restore-pending")
}

// Create 把当前数据库写成 zip 流。
//
// 内容：mmx.db（VACUUM INTO 快照）+ manifest.json。
func (m *Manager) Create(ctx context.Context, w io.Writer) error {
	if m.driver != "sqlite" {
		return fmt.Errorf("备份当前只支持 sqlite 驱动（当前 %s）", m.driver)
	}
	stageDir, err := os.MkdirTemp(m.dataDir, "backup-*")
	if err != nil {
		return fmt.Errorf("创建暂存目录失败: %w", err)
	}
	defer os.RemoveAll(stageDir)
	snapshot := filepath.Join(stageDir, "mmx.db")

	// VACUUM INTO 产出一致快照
	if _, err := m.db.ExecContext(ctx, `VACUUM INTO '`+strings.ReplaceAll(snapshot, "'", "''")+`'`); err != nil {
		return fmt.Errorf("生成数据库快照失败: %w", err)
	}

	manifest := Manifest{
		App: "mmx", Version: version.Version,
		CreatedAt: time.Now(), Driver: m.driver,
		Tables: m.tableCounts(ctx),
	}
	mraw, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}

	zw := zip.NewWriter(w)
	if err := writeZipFile(zw, "manifest.json", mraw); err != nil {
		_ = zw.Close()
		return err
	}
	f, err := os.Open(snapshot)
	if err != nil {
		_ = zw.Close()
		return err
	}
	defer f.Close()
	if err := writeZipReader(zw, "mmx.db", f); err != nil {
		_ = zw.Close()
		return err
	}
	return zw.Close()
}

// Restore 接收备份 zip，校验后落成待应用文件。
//
// 返回 manifest 供调用方展示，以及「是否需要重启」。
func (m *Manager) Restore(r io.Reader) (Manifest, error) {
	var manifest Manifest

	tmp, err := os.CreateTemp(m.dataDir, "restore-*.zip")
	if err != nil {
		return manifest, fmt.Errorf("创建暂存文件失败: %w", err)
	}
	defer func() {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
	}()
	// 限制 2 GiB，避免异常上传打爆磁盘
	if _, err := io.Copy(tmp, io.LimitReader(r, 2<<30)); err != nil {
		return manifest, fmt.Errorf("写入上传内容失败: %w", err)
	}
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		return manifest, err
	}
	st, err := tmp.Stat()
	if err != nil {
		return manifest, err
	}
	zr, err := zip.NewReader(tmp, st.Size())
	if err != nil {
		return manifest, fmt.Errorf("不是合法的 zip 备份包: %w", err)
	}

	var dbFound bool
	for _, f := range zr.File {
		switch filepath.Base(f.Name) {
		case "manifest.json":
			rc, err := f.Open()
			if err != nil {
				return manifest, err
			}
			raw, _ := io.ReadAll(io.LimitReader(rc, 1<<20))
			_ = rc.Close()
			if err := json.Unmarshal(raw, &manifest); err != nil {
				return manifest, fmt.Errorf("manifest 解析失败: %w", err)
			}
			if manifest.App != "" && manifest.App != "mmx" {
				return manifest, fmt.Errorf("备份包不属于 MMX（app=%q）", manifest.App)
			}
		case "mmx.db":
			rc, err := f.Open()
			if err != nil {
				return manifest, err
			}
			// 校验 SQLite 魔数，避免把任意文件当数据库塞进去
			header := make([]byte, 16)
			if _, err := io.ReadFull(rc, header); err != nil {
				_ = rc.Close()
				return manifest, errors.New("备份包内的 mmx.db 过小或损坏")
			}
			if string(header[:15]) != "SQLite format 3" {
				_ = rc.Close()
				return manifest, errors.New("备份包内的 mmx.db 不是合法的 SQLite 数据库")
			}
			dst := m.PendingRestorePath()
			out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
			if err != nil {
				_ = rc.Close()
				return manifest, fmt.Errorf("写入待恢复文件失败: %w", err)
			}
			if _, err := out.Write(header); err != nil {
				_ = out.Close()
				_ = rc.Close()
				return manifest, err
			}
			if _, err := io.Copy(out, io.LimitReader(rc, 2<<30)); err != nil {
				_ = out.Close()
				_ = rc.Close()
				return manifest, err
			}
			_ = out.Close()
			_ = rc.Close()
			dbFound = true
		}
	}
	if !dbFound {
		return manifest, errors.New("备份包里没有 mmx.db")
	}
	manifest.Note = "已落成 " + m.PendingRestorePath() + "，重启主控后生效"
	return manifest, nil
}

// ApplyPendingRestore 在启动时应用待恢复的数据库。
//
// 旧库会被重命名为 .pre-restore-<时间戳> 留档，便于回滚。
func ApplyPendingRestore(dataDir, dsn string) (bool, error) {
	pending := filepath.Join(dataDir, "mmx.db.restore-pending")
	if _, err := os.Stat(pending); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	target := dsn
	if !strings.HasPrefix(target, "/") {
		target = filepath.Join(dataDir, target)
	}
	// 清理可能残留的 WAL / SHM，否则新旧库会串
	for _, suffix := range []string{"-wal", "-shm"} {
		_ = os.Remove(target + suffix)
	}
	if _, err := os.Stat(target); err == nil {
		backupName := fmt.Sprintf("%s.pre-restore-%s", target, time.Now().Format("20060102-150405"))
		if err := os.Rename(target, backupName); err != nil {
			return false, fmt.Errorf("留档旧库失败: %w", err)
		}
	}
	if err := os.Rename(pending, target); err != nil {
		return false, fmt.Errorf("应用待恢复库失败: %w", err)
	}
	return true, nil
}

func (m *Manager) tableCounts(ctx context.Context) map[string]int64 {
	out := map[string]int64{}
	tables := []string{
		"users", "servers", "agent_tokens", "inbounds", "inbound_clients", "nodes",
		"packages", "announcements", "traffic_daily", "user_traffic_daily",
		"pending_tasks", "api_tokens", "audit_logs",
	}
	for _, t := range tables {
		var n int64
		if err := m.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+t).Scan(&n); err == nil {
			out[t] = n
		}
	}
	return out
}

func writeZipFile(zw *zip.Writer, name string, data []byte) error {
	hdr := &zip.FileHeader{Name: name, Method: zip.Deflate}
	hdr.Modified = time.Now()
	w, err := zw.CreateHeader(hdr)
	if err != nil {
		return err
	}
	_, err = w.Write(data)
	return err
}

func writeZipReader(zw *zip.Writer, name string, r io.Reader) error {
	hdr := &zip.FileHeader{Name: name, Method: zip.Deflate}
	hdr.Modified = time.Now()
	w, err := zw.CreateHeader(hdr)
	if err != nil {
		return err
	}
	_, err = io.Copy(w, r)
	return err
}
