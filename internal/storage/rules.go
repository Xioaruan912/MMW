package storage

import (
	"context"
	"database/sql"
	"errors"

	"github.com/mmx-project/mmx/internal/idgen"
)

// RuleTemplate 是一份订阅模板。
type RuleTemplate struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Content     string `json:"content"`
	Builtin     bool   `json:"builtin"`
	CreatedAt   int64  `json:"created_at"`
	UpdatedAt   int64  `json:"updated_at"`
}

// RuleProvider 是一条 Clash rule-provider 定义。
type RuleProvider struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Type      string `json:"type"`
	Behavior  string `json:"behavior"`
	Format    string `json:"format"`
	URL       string `json:"url"`
	Path      string `json:"path"`
	Interval  int    `json:"interval"`
	Enabled   bool   `json:"enabled"`
	CreatedAt int64  `json:"created_at"`
	UpdatedAt int64  `json:"updated_at"`
}

// SubscribeFile 是一份可直接下发的订阅文件（用户上传或系统生成）。
type SubscribeFile struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Content   string `json:"content"`
	Enabled   bool   `json:"enabled"`
	CreatedAt int64  `json:"created_at"`
	UpdatedAt int64  `json:"updated_at"`
}

const tmplCols = `id,name,description,content,builtin,created_at,updated_at`
const provCols = `id,name,type,behavior,format,url,path,interval,enabled,created_at,updated_at`
const subfileCols = `id,name,content,enabled,created_at,updated_at`

// ---------------------------------------------------------------------------
// 模板
// ---------------------------------------------------------------------------

// CreateRuleTemplate 新建模板。
func (s *Store) CreateRuleTemplate(ctx context.Context, t RuleTemplate) (RuleTemplate, error) {
	if t.ID == "" {
		t.ID = idgen.New("tpl")
	}
	now := nowMS()
	t.CreatedAt, t.UpdatedAt = now, now
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO rule_templates(`+tmplCols+`) VALUES(?,?,?,?,?,?,?)`,
		t.ID, t.Name, t.Description, t.Content, b2i(t.Builtin), t.CreatedAt, t.UpdatedAt)
	return t, err
}

// GetRuleTemplate 按 ID 取模板。
func (s *Store) GetRuleTemplate(ctx context.Context, id string) (RuleTemplate, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+tmplCols+` FROM rule_templates WHERE id=?`, id)
	var t RuleTemplate
	var builtin int
	err := row.Scan(&t.ID, &t.Name, &t.Description, &t.Content, &builtin, &t.CreatedAt, &t.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return RuleTemplate{}, ErrNotFound
	}
	t.Builtin = builtin != 0
	return t, err
}

// GetRuleTemplateByName 按名字取模板。
func (s *Store) GetRuleTemplateByName(ctx context.Context, name string) (RuleTemplate, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+tmplCols+` FROM rule_templates WHERE name=?`, name)
	var t RuleTemplate
	var builtin int
	err := row.Scan(&t.ID, &t.Name, &t.Description, &t.Content, &builtin, &t.CreatedAt, &t.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return RuleTemplate{}, ErrNotFound
	}
	t.Builtin = builtin != 0
	return t, err
}

// ListRuleTemplates 列出模板。
func (s *Store) ListRuleTemplates(ctx context.Context) ([]RuleTemplate, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+tmplCols+` FROM rule_templates ORDER BY builtin DESC, created_at ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RuleTemplate
	for rows.Next() {
		var t RuleTemplate
		var builtin int
		if err := rows.Scan(&t.ID, &t.Name, &t.Description, &t.Content, &builtin,
			&t.CreatedAt, &t.UpdatedAt); err != nil {
			return nil, err
		}
		t.Builtin = builtin != 0
		out = append(out, t)
	}
	return out, rows.Err()
}

// UpdateRuleTemplate 更新模板（内置模板也可改，便于就地微调）。
func (s *Store) UpdateRuleTemplate(ctx context.Context, t RuleTemplate) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE rule_templates SET name=?, description=?, content=?, updated_at=? WHERE id=?`,
		t.Name, t.Description, t.Content, nowMS(), t.ID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteRuleTemplate 删除模板。内置模板不允许删除。
func (s *Store) DeleteRuleTemplate(ctx context.Context, id string) error {
	t, err := s.GetRuleTemplate(ctx, id)
	if err != nil {
		return err
	}
	if t.Builtin {
		return errors.New("内置模板不可删除；请复制后再修改")
	}
	res, err := s.db.ExecContext(ctx, `DELETE FROM rule_templates WHERE id=?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// ---------------------------------------------------------------------------
// 规则集
// ---------------------------------------------------------------------------

// CreateRuleProvider 新建规则集。
func (s *Store) CreateRuleProvider(ctx context.Context, p RuleProvider) (RuleProvider, error) {
	if p.ID == "" {
		p.ID = idgen.New("rpv")
	}
	if p.Type == "" {
		p.Type = "http"
	}
	if p.Behavior == "" {
		p.Behavior = "classical"
	}
	if p.Format == "" {
		p.Format = "yaml"
	}
	if p.Interval <= 0 {
		p.Interval = 86400
	}
	now := nowMS()
	p.CreatedAt, p.UpdatedAt = now, now
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO rule_providers(`+provCols+`) VALUES(?,?,?,?,?,?,?,?,?,?,?)`,
		p.ID, p.Name, p.Type, p.Behavior, p.Format, p.URL, p.Path, p.Interval,
		b2i(p.Enabled), p.CreatedAt, p.UpdatedAt)
	return p, err
}

// ListRuleProviders 列出规则集。
func (s *Store) ListRuleProviders(ctx context.Context) ([]RuleProvider, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+provCols+` FROM rule_providers ORDER BY name ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RuleProvider
	for rows.Next() {
		var p RuleProvider
		var enabled int
		if err := rows.Scan(&p.ID, &p.Name, &p.Type, &p.Behavior, &p.Format, &p.URL, &p.Path,
			&p.Interval, &enabled, &p.CreatedAt, &p.UpdatedAt); err != nil {
			return nil, err
		}
		p.Enabled = enabled != 0
		out = append(out, p)
	}
	return out, rows.Err()
}

// DeleteRuleProvider 删除规则集。
func (s *Store) DeleteRuleProvider(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM rule_providers WHERE id=?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// ---------------------------------------------------------------------------
// 订阅文件
// ---------------------------------------------------------------------------

// CreateSubscribeFile 新建订阅文件。
func (s *Store) CreateSubscribeFile(ctx context.Context, f SubscribeFile) (SubscribeFile, error) {
	if f.ID == "" {
		f.ID = idgen.New("sub")
	}
	now := nowMS()
	f.CreatedAt, f.UpdatedAt = now, now
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO subscribe_files(`+subfileCols+`) VALUES(?,?,?,?,?,?)`,
		f.ID, f.Name, f.Content, b2i(f.Enabled), f.CreatedAt, f.UpdatedAt)
	return f, err
}

// ListSubscribeFiles 列出订阅文件（不含内容，避免列表接口过大）。
func (s *Store) ListSubscribeFiles(ctx context.Context) ([]SubscribeFile, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id,name,'',enabled,created_at,updated_at FROM subscribe_files ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SubscribeFile
	for rows.Next() {
		var f SubscribeFile
		var enabled int
		if err := rows.Scan(&f.ID, &f.Name, &f.Content, &enabled, &f.CreatedAt, &f.UpdatedAt); err != nil {
			return nil, err
		}
		f.Enabled = enabled != 0
		out = append(out, f)
	}
	return out, rows.Err()
}

// GetSubscribeFile 取订阅文件（含内容）。
func (s *Store) GetSubscribeFile(ctx context.Context, id string) (SubscribeFile, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+subfileCols+` FROM subscribe_files WHERE id=?`, id)
	var f SubscribeFile
	var enabled int
	err := row.Scan(&f.ID, &f.Name, &f.Content, &enabled, &f.CreatedAt, &f.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return SubscribeFile{}, ErrNotFound
	}
	f.Enabled = enabled != 0
	return f, err
}

// DeleteSubscribeFile 删除订阅文件。
func (s *Store) DeleteSubscribeFile(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM subscribe_files WHERE id=?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// SetUserTemplate 设置用户订阅所用的模板 ID（空表示用默认）。
func (s *Store) SetUserTemplate(ctx context.Context, userID, templateID string) error {
	res, err := s.db.ExecContext(ctx, `UPDATE users SET template_id=?, updated_at=? WHERE id=?`,
		templateID, nowMS(), userID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}
