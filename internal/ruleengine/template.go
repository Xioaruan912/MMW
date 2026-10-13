// Package ruleengine 提供订阅模板引擎。
//
// 模板是管理员可编辑的 Clash 配置骨架：顶层的 dns / tun / 各类开关，
// 加上代理组（proxy-groups）与规则（rules）。渲染时把实际节点注入进去。
//
// 为什么不用通用模板语法（如 text/template）：
// 配置里大量出现 `{{ }}`、`$`、反引号等字符，通用模板引擎容易与之冲突，
// 且报错信息对运维不友好。这里只保留两个占位符：
//
//	{{PROXIES}}      —— 展开为完整的 YAML 代理列表（缩进已处理好）
//	{{PROXY_NAMES}}  —— 展开为 YAML 字符串列表（用于 proxy-groups 的 proxies 字段）
//
// 这样模板本身就是一份**可直接粘贴进 Clash 使用**的配置，只是节点位置留白。
package ruleengine

import (
	"errors"
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/mmx-project/mmx/internal/storage"
)

// 占位符。
const (
	PlaceholderProxies    = "{{PROXIES}}"
	PlaceholderProxyNames = "{{PROXY_NAMES}}"
)

// Template 是订阅模板。
//
// 直接复用数据层的类型（类型别名）而不是再定义一遍：
// 两套结构体之间来回转换是纯粹的样板代码，且早晚会因为加字段漏了一边而不一致。
type Template = storage.RuleTemplate

// Validate 检查模板是否可用。
//
// 必须显式包含节点占位符 —— 否则渲染出来是空配置，
// 用户导入后"连不上"，而错误却出现在客户端而不是主控，排障成本极高。
func Validate(t Template) error {
	if strings.TrimSpace(t.Name) == "" {
		return errors.New("模板名称不能为空")
	}
	if strings.TrimSpace(t.Content) == "" {
		return errors.New("模板内容不能为空")
	}
	if !strings.Contains(t.Content, PlaceholderProxies) {
		return fmt.Errorf("模板必须包含 %s 占位符（用于注入节点）", PlaceholderProxies)
	}
	// 语法校验：把占位符临时替换成合法 YAML 再解析
	if _, err := parseTemplateYAML(t.Content); err != nil {
		return fmt.Errorf("模板不是合法的 YAML: %w\n"+
			"提示：%s 与 %s 必须**独占一行**，且缩进要和它所在列表的同级项对齐"+
			"（例如写在 proxy-groups 的 proxies: 下时应与上面的 `- AUTO` 同列）",
			err, PlaceholderProxies, PlaceholderProxyNames)
	}
	return nil
}

// parseTemplateYAML 把占位符替换为占位内容后解析，用于语法校验。
//
// 必须用与真实渲染**完全相同**的替换逻辑（含缩进处理），
// 否则校验通过、渲染却坏掉。
func parseTemplateYAML(content string) (map[string]any, error) {
	probe := replacePlaceholder(content, PlaceholderProxies,
		"- {name: __probe__, type: direct}")
	probe = replacePlaceholder(probe, PlaceholderProxyNames, "- __probe__")
	var doc map[string]any
	if err := yaml.Unmarshal([]byte(probe), &doc); err != nil {
		return nil, err
	}
	return doc, nil
}

// RenderOptions 控制渲染行为。
type RenderOptions struct {
	// ProxyGroupName 是默认主选择组的名字，用于 fallback（模板里没写组时）。
	ProxyGroupName string
}

// Render 把代理列表注入模板。
//
// proxies 是已经构造好的 Clash 代理条目；names 是对应的名字列表。
func Render(tmpl Template, proxies []map[string]any, names []string, opts RenderOptions) ([]byte, error) {
	if err := Validate(tmpl); err != nil {
		return nil, err
	}
	if opts.ProxyGroupName == "" {
		opts.ProxyGroupName = "PROXY"
	}

	proxiesYAML, err := yamlList(proxies)
	if err != nil {
		return nil, fmt.Errorf("序列化节点失败: %w", err)
	}
	namesYAML, err := yamlList(names)
	if err != nil {
		return nil, fmt.Errorf("序列化节点名失败: %w", err)
	}

	out := replacePlaceholder(tmpl.Content, PlaceholderProxies, proxiesYAML)
	out = replacePlaceholder(out, PlaceholderProxyNames, namesYAML)

	// 渲染后必须仍是合法 YAML —— 否则用户拿到的是坏配置
	var doc map[string]any
	if err := yaml.Unmarshal([]byte(out), &doc); err != nil {
		return nil, fmt.Errorf("渲染结果不是合法 YAML（模板缩进或占位符位置有问题）: %w", err)
	}
	if _, ok := doc["proxies"]; !ok {
		return nil, errors.New("渲染结果缺少 proxies 段（占位符可能放在了错误的层级）")
	}
	return []byte(out), nil
}

// yamlList 把一个切片序列化成**无缩进**的 YAML 列表。
//
// 缩进由 replacePlaceholder 根据占位符所在的列决定 ——
// 占位符可能出现在顶层（proxies:）也可能在嵌套层（proxy-groups[].proxies:），
// 把缩进写死在这里必然会在某一层被打破。
func yamlList(v any) (string, error) {
	if v == nil {
		return "[]", nil
	}
	raw, err := yaml.Marshal(v)
	if err != nil {
		return "", err
	}
	return strings.TrimRight(string(raw), "\n"), nil
}

// replacePlaceholder 把整行占位符替换为生成的 YAML，并保持其所在列的缩进。
//
// 约定：占位符独占一行（允许前后有空白）。这是模板作者唯一需要遵守的规则。
func replacePlaceholder(content, placeholder, generated string) string {
	lines := strings.Split(content, "\n")
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		trimmed := strings.TrimLeft(line, " \t")
		if !strings.HasPrefix(trimmed, placeholder) || strings.TrimSpace(strings.TrimPrefix(trimmed, placeholder)) != "" {
			out = append(out, line)
			continue
		}
		indent := line[:len(line)-len(trimmed)]
		if generated == "" {
			continue
		}
		// 空列表必须仍缩进：YAML 里 `proxies:` 后面跟一个顶格的 `[]` 是非法的
		// （会被当成新的顶层节点）。给两格缩进就变成合法的块内流式序列。
		if generated == "[]" && indent == "" {
			indent = "  "
		}
		for _, g := range strings.Split(generated, "\n") {
			out = append(out, indent+g)
		}
	}
	return strings.Join(out, "\n")
}

// SubstoreProducer 是订阅文件生成器的注册名（与 subscriptions 功能对齐）。
type SubstoreProducer string

// 内置模板名。
const (
	BuiltinSimple  = "simple"
	BuiltinFull    = "full"
	BuiltinMinimal = "minimal"
)
