package ruleengine

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func sampleProxies() ([]map[string]any, []string) {
	return []map[string]any{
		{"name": "香港-01", "type": "vless", "server": "1.2.3.4", "port": 443, "uuid": "u1"},
		{"name": "日本-01", "type": "trojan", "server": "5.6.7.8", "port": 8443, "password": "p1"},
	}, []string{"香港-01", "日本-01"}
}

func TestBuiltinTemplatesAllValid(t *testing.T) {
	all := BuiltinTemplates()
	if len(all) == 0 {
		t.Fatal("应当有内置模板")
	}
	for _, tmpl := range all {
		if err := Validate(tmpl); err != nil {
			t.Errorf("内置模板 %s 校验失败: %v", tmpl.Name, err)
		}
		if !tmpl.Builtin {
			t.Errorf("内置模板 %s 未标记 Builtin", tmpl.Name)
		}
		if tmpl.Description == "" {
			t.Errorf("内置模板 %s 缺少说明", tmpl.Name)
		}
	}
}

func TestBuiltinTemplatesRender(t *testing.T) {
	proxies, names := sampleProxies()
	for _, tmpl := range BuiltinTemplates() {
		t.Run(tmpl.Name, func(t *testing.T) {
			body, err := Render(tmpl, proxies, names, RenderOptions{})
			if err != nil {
				t.Fatalf("渲染失败: %v", err)
			}
			s := string(body)
			if strings.Contains(s, PlaceholderProxies) || strings.Contains(s, PlaceholderProxyNames) {
				t.Fatal("渲染结果中仍存在未替换的占位符")
			}
			// 结果必须是合法 YAML 且含 proxies / proxy-groups
			var doc map[string]any
			if err := yaml.Unmarshal(body, &doc); err != nil {
				t.Fatalf("渲染结果不是合法 YAML: %v\n%s", err, s)
			}
			if _, ok := doc["proxies"]; !ok {
				t.Fatalf("缺少 proxies 段:\n%s", s)
			}
			if _, ok := doc["proxy-groups"]; !ok {
				t.Fatalf("缺少 proxy-groups 段:\n%s", s)
			}
			// 节点名必须出现在结果里
			for _, n := range names {
				if !strings.Contains(s, n) {
					t.Fatalf("节点 %s 未出现在渲染结果中", n)
				}
			}
			// proxies 必须是列表且长度为节点数
			list, ok := doc["proxies"].([]any)
			if !ok || len(list) != len(proxies) {
				t.Fatalf("proxies 段落结构不对: %+v", doc["proxies"])
			}
		})
	}
}

func TestValidateRejectsMissingPlaceholder(t *testing.T) {
	// 没有节点占位符 → 渲染出来会是空配置，用户导入后连不上
	err := Validate(Template{
		Name:    "坏模板",
		Content: "proxies: []\nproxy-groups: []\nrules: []\n",
	})
	if err == nil {
		t.Fatal("缺少节点占位符必须被拒")
	}
	if !strings.Contains(err.Error(), PlaceholderProxies) {
		t.Fatalf("错误信息应指出缺少哪个占位符: %v", err)
	}
}

func TestValidateRejectsEmptyFields(t *testing.T) {
	if err := Validate(Template{Name: "", Content: minimalTemplate}); err == nil {
		t.Fatal("空名称必须被拒")
	}
	if err := Validate(Template{Name: "x", Content: "  "}); err == nil {
		t.Fatal("空内容必须被拒")
	}
}

func TestValidateRejectsBadYAML(t *testing.T) {
	bad := "proxies:\n" + PlaceholderProxies + "\n  \t- broken: [unclosed\n"
	if err := Validate(Template{Name: "坏 YAML", Content: bad}); err == nil {
		t.Fatal("非法 YAML 必须被拒")
	}
}

func TestRenderRejectsPlaceholderNotAloneOnLine(t *testing.T) {
	// 占位符与其它内容写在同一行 → 渲染后必然产生非法 YAML，必须报错
	bad := "mixed-port: 7890\nproxies: {{PROXIES}}\nproxy-groups: []\n"
	_, err := Render(Template{Name: "同行占位符", Content: bad},
		[]map[string]any{{"name": "a", "type": "vless"}}, []string{"a"}, RenderOptions{})
	if err == nil {
		t.Fatal("占位符与其它内容同行时应报错")
	}
}

func TestRenderHandlesEmptyNodeList(t *testing.T) {
	// 没有节点也要产出合法配置（只是 proxies 为空），不能 panic 也不能产出坏 YAML
	body, err := Render(Template{Name: "minimal", Content: minimalTemplate}, nil, nil, RenderOptions{})
	if err != nil {
		t.Fatalf("空节点列表渲染失败: %v", err)
	}
	var doc map[string]any
	if err := yaml.Unmarshal(body, &doc); err != nil {
		t.Fatalf("渲染结果不是合法 YAML: %v", err)
	}
}

func TestFindBuiltin(t *testing.T) {
	for _, name := range []string{BuiltinSimple, BuiltinFull, BuiltinMinimal} {
		if _, ok := FindBuiltin(name); !ok {
			t.Errorf("找不到内置模板 %s", name)
		}
	}
	if _, ok := FindBuiltin("nonexistent"); ok {
		t.Fatal("不存在的模板不应被找到")
	}
}

// TestReplacePlaceholderKeepsIndent 是最关键的一条：
// 占位符在嵌套层（proxy-groups[].proxies:）时必须跟随该层缩进，
// 否则列表会穿出父级，产出「校验通过、导入即报错」的配置。
func TestReplacePlaceholderKeepsIndent(t *testing.T) {
	content := "proxy-groups:\n  - name: PROXY\n    proxies:\n      {{PROXY_NAMES}}\n"
	out := replacePlaceholder(content, PlaceholderProxyNames, "- a\n- b")
	want := "proxy-groups:\n  - name: PROXY\n    proxies:\n      - a\n      - b\n"
	if out != want {
		t.Fatalf("缩进未跟随占位符所在列\n实得:\n%s\n期望:\n%s", out, want)
	}
}

// TestReplacePlaceholderAtTopLevel 顶层占位符不加缩进。
func TestReplacePlaceholderAtTopLevel(t *testing.T) {
	content := "proxies:\n{{PROXIES}}\n"
	out := replacePlaceholder(content, PlaceholderProxies, "- {name: a}")
	if out != "proxies:\n- {name: a}\n" {
		t.Fatalf("顶层占位符不应加缩进: %q", out)
	}
}

// TestReplacePlaceholderIgnoresInline 同行占用时不替换（交给后续 YAML 校验报错）。
func TestReplacePlaceholderIgnoresInline(t *testing.T) {
	content := "proxies: {{PROXIES}}\n"
	out := replacePlaceholder(content, PlaceholderProxies, "- a")
	if out != content {
		t.Fatalf("同行占位符不应被替换: %q", out)
	}
}
