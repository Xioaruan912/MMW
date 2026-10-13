package backup

import (
	"archive/zip"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

// newTestDB 建一个真实的 SQLite 库与 Manager。
func newTestDB(t *testing.T) (*Manager, *sql.DB, string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "mmx.db")
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=journal_mode(WAL)")
	if err != nil {
		t.Fatalf("打开测试库失败: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	// 建一张有实际数据的表，验证快照内容而不只是文件存在
	if _, err := db.Exec(`CREATE TABLE users(id TEXT PRIMARY KEY, username TEXT)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO users(id,username) VALUES('u1','alice'),('u2','bob')`); err != nil {
		t.Fatal(err)
	}
	return NewManager(db, dir, "sqlite"), db, dir
}

func TestCreateProducesValidZip(t *testing.T) {
	mgr, _, _ := newTestDB(t)

	var buf bytes.Buffer
	if err := mgr.Create(context.Background(), &buf); err != nil {
		t.Fatalf("备份失败: %v", err)
	}
	if buf.Len() == 0 {
		t.Fatal("备份内容为空")
	}

	zr, err := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if err != nil {
		t.Fatalf("备份不是合法 zip: %v", err)
	}

	var hasDB, hasManifest bool
	var manifest Manifest
	for _, f := range zr.File {
		switch f.Name {
		case "mmx.db":
			hasDB = true
			rc, _ := f.Open()
			hdr := make([]byte, 15)
			_, _ = rc.Read(hdr)
			_ = rc.Close()
			if string(hdr) != "SQLite format 3" {
				t.Fatalf("备份内的数据库魔数不对: %q", hdr)
			}
		case "manifest.json":
			hasManifest = true
			rc, _ := f.Open()
			raw := make([]byte, f.UncompressedSize64)
			_, _ = rc.Read(raw)
			_ = rc.Close()
			if err := json.Unmarshal(raw, &manifest); err != nil {
				t.Fatalf("manifest 不是合法 JSON: %v", err)
			}
		}
	}
	if !hasDB {
		t.Fatal("备份包内缺少 mmx.db")
	}
	if !hasManifest {
		t.Fatal("备份包内缺少 manifest.json")
	}
	if manifest.App != "mmx" {
		t.Fatalf("manifest.app 应为 mmx，实得 %q", manifest.App)
	}
	if manifest.CreatedAt.IsZero() {
		t.Fatal("manifest 缺少创建时间")
	}
	// 表计数应当反映出真实数据
	if manifest.Tables["users"] != 2 {
		t.Fatalf("manifest 表计数错误: users=%d", manifest.Tables["users"])
	}
}

func TestRestoreStageAndApply(t *testing.T) {
	mgr, db, dir := newTestDB(t)

	var buf bytes.Buffer
	if err := mgr.Create(context.Background(), &buf); err != nil {
		t.Fatal(err)
	}

	// 备份后再改数据，恢复后应当回到备份时的状态
	if _, err := db.Exec(`INSERT INTO users(id,username) VALUES('u3','carol')`); err != nil {
		t.Fatal(err)
	}

	manifest, err := mgr.Restore(bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatalf("恢复失败: %v", err)
	}
	if manifest.Note == "" {
		t.Fatal("恢复结果应说明如何生效")
	}
	pending := mgr.PendingRestorePath()
	if _, err := os.Stat(pending); err != nil {
		t.Fatalf("待恢复文件未落盘: %v", err)
	}

	// 应用恢复：必须把旧库留档
	applied, err := ApplyPendingRestore(dir, filepath.Join(dir, "mmx.db"))
	if err != nil {
		t.Fatalf("应用恢复失败: %v", err)
	}
	if !applied {
		t.Fatal("应当报告已应用")
	}
	matches, _ := filepath.Glob(filepath.Join(dir, "mmx.db.pre-restore-*"))
	if len(matches) != 1 {
		t.Fatalf("旧库应当被留档 1 份，实得 %d", len(matches))
	}

	// 二次调用应当无事发生
	applied2, err := ApplyPendingRestore(dir, filepath.Join(dir, "mmx.db"))
	if err != nil || applied2 {
		t.Fatalf("没有待恢复文件时应返回 (false,nil)，实得 (%v,%v)", applied2, err)
	}
}

func TestRestoreRejectsGarbage(t *testing.T) {
	mgr, _, _ := newTestDB(t)

	if _, err := mgr.Restore(bytes.NewReader([]byte("not a zip at all"))); err == nil {
		t.Fatal("非 zip 输入必须被拒")
	}

	// 合法 zip 但没有 mmx.db
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, _ := zw.Create("readme.txt")
	_, _ = w.Write([]byte("hello"))
	_ = zw.Close()
	if _, err := mgr.Restore(bytes.NewReader(buf.Bytes())); err == nil {
		t.Fatal("缺少 mmx.db 的包必须被拒")
	}

	// 有 mmx.db 但不是 SQLite
	var buf2 bytes.Buffer
	zw2 := zip.NewWriter(&buf2)
	w2, _ := zw2.Create("mmx.db")
	_, _ = w2.Write([]byte("this is definitely not sqlite, but long enough"))
	_ = zw2.Close()
	if _, err := mgr.Restore(bytes.NewReader(buf2.Bytes())); err == nil {
		t.Fatal("伪造的 mmx.db 必须被 SQLite 魔数校验拦下")
	}
}

func TestRestoreRejectsForeignManifest(t *testing.T) {
	mgr, _, _ := newTestDB(t)

	// 构造一个 app 字段不是 mmx 的包
	src := Manifest{App: "other-app", Version: "1.0.0"}
	raw, _ := json.Marshal(src)

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, _ := zw.Create("manifest.json")
	_, _ = w.Write(raw)
	w2, _ := zw.Create("mmx.db")
	_, _ = w2.Write([]byte("SQLite format 3\x00padding padding padding"))
	_ = zw.Close()

	if _, err := mgr.Restore(bytes.NewReader(buf.Bytes())); err == nil {
		t.Fatal("外来备份包必须被拒")
	}
}
