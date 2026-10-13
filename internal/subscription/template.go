package subscription

import (
	"fmt"

	"github.com/mmx-project/mmx/internal/ruleengine"
)

// RenderClashWithTemplate 用管理员配置的模板渲染 Clash 配置。
//
// 与 RenderClash 的区别：proxy-groups 与 rules 完全由模板决定，
// 这里只负责把节点列表塞进占位符。
func RenderClashWithTemplate(entries []Entry, tmpl ruleengine.Template, opts ClashOptions) ([]byte, error) {
	if opts.ProxyGroupName == "" {
		opts.ProxyGroupName = "PROXY"
	}
	proxies := make([]map[string]any, 0, len(entries))
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		p, err := clashProxy(e)
		if err != nil {
			return nil, fmt.Errorf("节点 %s 渲染失败: %w", e.Name(), err)
		}
		proxies = append(proxies, p)
		names = append(names, sanitizeName(e.Name()))
	}
	return ruleengine.Render(tmpl, proxies, names, ruleengine.RenderOptions{
		ProxyGroupName: opts.ProxyGroupName,
	})
}
