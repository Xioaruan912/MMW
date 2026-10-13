package ruleengine

import (
	"context"
	"log/slog"

	"github.com/mmx-project/mmx/internal/storage"
)

// EnsureBuiltins 把内置模板写入数据库（已存在则跳过）。
//
// 内置模板落库而不是只在内存里：管理员要在面板里看到它们、基于它们复制修改，
// 而"渲染"路径也需要能按 ID 统一取到模板。
func EnsureBuiltins(ctx context.Context, store *storage.Store, log *slog.Logger) error {
	existing, err := store.ListRuleTemplates(ctx)
	if err != nil {
		return err
	}
	have := map[string]bool{}
	for _, t := range existing {
		have[t.Name] = true
	}
	for _, t := range BuiltinTemplates() {
		if have[t.Name] {
			continue
		}
		if _, err := store.CreateRuleTemplate(ctx, t); err != nil {
			return err
		}
		if log != nil {
			log.Info("已写入内置订阅模板", "name", t.Name)
		}
	}
	return nil
}
