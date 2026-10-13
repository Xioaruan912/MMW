package mcp

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mmx-project/mmx/internal/storage"
)

func newTestServer(t *testing.T) (*Server, *storage.Store) {
	t.Helper()
	db, err := storage.Open("sqlite", filepath.Join(t.TempDir(), "mcp.db"))
	if err != nil {
		t.Fatalf("打开测试库失败: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	st, err := storage.New(context.Background(), db)
	if err != nil {
		t.Fatalf("迁移失败: %v", err)
	}
	deps := Deps{
		Store:  st,
		Online: func(string) bool { return false },
	}
	srv := New(slog.New(slog.NewTextHandler(io.Discard, nil)), Tools(deps))
	return srv, st
}

func call(t *testing.T, s *Server, body string, id Identity) map[string]any {
	t.Helper()
	resp := s.Handle(context.Background(), []byte(body), id)
	if resp == nil {
		t.Fatal("期望有响应，实得 nil（通知类消息不应出现在这里）")
	}
	raw, _ := json.Marshal(resp)
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("响应不是合法 JSON: %v", err)
	}
	return out
}

func admin() Identity {
	return Identity{UserID: "u1", Username: "admin", Role: storage.RoleAdmin, IsAdmin: true}
}
func normal() Identity {
	return Identity{UserID: "u2", Username: "user", Role: storage.RoleUser, IsAdmin: false}
}

func TestInitializeEchoesSupportedVersion(t *testing.T) {
	s, _ := newTestServer(t)
	out := call(t, s, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05"}}`, admin())
	res, ok := out["result"].(map[string]any)
	if !ok {
		t.Fatalf("缺少 result: %+v", out)
	}
	if res["protocolVersion"] != "2024-11-05" {
		t.Fatalf("协议版本未回选: %v", res["protocolVersion"])
	}
	caps, _ := res["capabilities"].(map[string]any)
	if _, ok := caps["tools"]; !ok {
		t.Fatal("缺少 tools 能力声明")
	}
	if res["serverInfo"] == nil {
		t.Fatal("缺少 serverInfo")
	}
}

func TestInitializeFallsBackForUnknownVersion(t *testing.T) {
	s, _ := newTestServer(t)
	out := call(t, s, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"1999-01-01"}}`, admin())
	res := out["result"].(map[string]any)
	if res["protocolVersion"] != ProtocolVersion {
		t.Fatalf("未知版本应回落到服务端默认版本，实得 %v", res["protocolVersion"])
	}
}

func TestToolsListHidesAdminToolsFromNormalUser(t *testing.T) {
	s, _ := newTestServer(t)

	adminOut := call(t, s, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`, admin())
	adminTools := adminOut["result"].(map[string]any)["tools"].([]any)

	userOut := call(t, s, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`, normal())
	userTools := userOut["result"].(map[string]any)["tools"].([]any)

	if len(adminTools) <= len(userTools) {
		t.Fatalf("管理员应看到更多工具：admin=%d user=%d", len(adminTools), len(userTools))
	}
	// 普通用户不应看到任何 AdminOnly 工具
	for _, raw := range userTools {
		item := raw.(map[string]any)
		name := item["name"].(string)
		if t0, ok := s.tools[name]; ok && t0.AdminOnly {
			t.Fatalf("普通用户看到了管理员工具 %s", name)
		}
		if item["inputSchema"] == nil {
			t.Fatalf("工具 %s 缺少 inputSchema", name)
		}
	}
}

func TestToolsCallRejectsAdminToolForNormalUser(t *testing.T) {
	s, _ := newTestServer(t)
	out := call(t, s, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"create_user","arguments":{"username":"x","password":"1234567890"}}}`, normal())
	res := out["result"].(map[string]any)
	if res["isError"] != true {
		t.Fatalf("普通用户调用管理员工具应返回 isError: %+v", res)
	}
	text := res["content"].([]any)[0].(map[string]any)["text"].(string)
	if !strings.Contains(text, "权限不足") {
		t.Fatalf("错误信息应说明权限不足: %s", text)
	}
}

func TestToolsCallUnknownTool(t *testing.T) {
	s, _ := newTestServer(t)
	out := call(t, s, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"nope"}}`, admin())
	if out["error"] == nil {
		t.Fatal("未知工具应返回 JSON-RPC 错误")
	}
}

func TestOverviewAndListTools(t *testing.T) {
	s, st := newTestServer(t)
	ctx := context.Background()
	if _, err := st.CreateServer(ctx, "n1", "1.2.3.4", "", storage.XrayModeExternal); err != nil {
		t.Fatal(err)
	}

	out := call(t, s, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"get_overview"}}`, admin())
	res := out["result"].(map[string]any)
	if res["isError"] == true {
		t.Fatalf("get_overview 失败: %+v", res)
	}
	text := res["content"].([]any)[0].(map[string]any)["text"].(string)
	if !strings.Contains(text, `"servers_total": 1`) {
		t.Fatalf("总览数据不对: %s", text)
	}

	out = call(t, s, `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"list_servers"}}`, admin())
	text = out["result"].(map[string]any)["content"].([]any)[0].(map[string]any)["text"].(string)
	if !strings.Contains(text, "n1") {
		t.Fatalf("服务器列表不含新建的服务器: %s", text)
	}
}

func TestCreateUserViaTool(t *testing.T) {
	s, st := newTestServer(t)
	out := call(t, s, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"create_user","arguments":{"username":"alice","password":"Alice@12345","nickname":"Alice"}}}`, admin())
	res := out["result"].(map[string]any)
	if res["isError"] == true {
		t.Fatalf("创建用户失败: %+v", res)
	}
	u, err := st.GetUserByUsername(context.Background(), "alice")
	if err != nil {
		t.Fatalf("用户未落库: %v", err)
	}
	if u.Nickname != "Alice" {
		t.Fatalf("昵称未生效: %q", u.Nickname)
	}
}

func TestCreateUserRejectsShortPassword(t *testing.T) {
	s, _ := newTestServer(t)
	out := call(t, s, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"create_user","arguments":{"username":"bob","password":"short"}}}`, admin())
	res := out["result"].(map[string]any)
	if res["isError"] != true {
		t.Fatal("过短口令必须被拒")
	}
}

func TestCreateInboundViaTool(t *testing.T) {
	s, st := newTestServer(t)
	ctx := context.Background()
	srv, _ := st.CreateServer(ctx, "n1", "1.2.3.4", "", storage.XrayModeExternal)

	body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"create_inbound","arguments":{
      "server_id":"` + srv.ID + `","tag":"in-443","protocol":"vless","transport":"tcp",
      "security":"tls","port":443,"node_name":"香港-01",
      "stream":{"server_name":"a.example.com"},
      "settings":{"flow":"xtls-rprx-vision"}}}}`
	out := call(t, s, body, admin())
	res := out["result"].(map[string]any)
	if res["isError"] == true {
		t.Fatalf("创建入站失败: %+v", res)
	}
	inbounds, _ := st.ListInbounds(ctx, srv.ID)
	if len(inbounds) != 1 || inbounds[0].Tag != "in-443" {
		t.Fatalf("入站未落库: %+v", inbounds)
	}
	if inbounds[0].Security != "tls" {
		t.Fatalf("安全层未生效: %q", inbounds[0].Security)
	}
}

func TestCreateInboundRejectsRealityOverWS(t *testing.T) {
	s, st := newTestServer(t)
	ctx := context.Background()
	srv, _ := st.CreateServer(ctx, "n1", "", "", storage.XrayModeExternal)

	body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"create_inbound","arguments":{
      "server_id":"` + srv.ID + `","tag":"bad","protocol":"vless","transport":"ws",
      "security":"reality","port":443,
      "stream":{"reality_dest":"a.example.com:443","reality_server_names":["a.example.com"],"reality_private_key":"k"}}}}`
	out := call(t, s, body, admin())
	res := out["result"].(map[string]any)
	if res["isError"] != true {
		t.Fatal("REALITY + WS 必须被拒")
	}
	text := res["content"].([]any)[0].(map[string]any)["text"].(string)
	if !strings.Contains(text, "REALITY") {
		t.Fatalf("错误信息应说明 REALITY 限制: %s", text)
	}
}

func TestDispatchTaskRejectsUnknownKind(t *testing.T) {
	s, st := newTestServer(t)
	srv, _ := st.CreateServer(context.Background(), "n1", "", "", storage.XrayModeExternal)
	out := call(t, s, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"dispatch_task","arguments":{"server_id":"`+srv.ID+`","kind":"shell.exec"}}}`, admin())
	res := out["result"].(map[string]any)
	if res["isError"] != true {
		t.Fatal("非白名单任务必须被拒")
	}
	text := res["content"].([]any)[0].(map[string]any)["text"].(string)
	if !strings.Contains(text, "白名单") {
		t.Fatalf("错误信息应提到白名单: %s", text)
	}
}

func TestParseErrorAndUnknownMethod(t *testing.T) {
	s, _ := newTestServer(t)
	out := call(t, s, `{not json`, admin())
	if out["error"] == nil {
		t.Fatal("非法 JSON 应返回 parse error")
	}
	out = call(t, s, `{"jsonrpc":"2.0","id":1,"method":"does/not/exist"}`, admin())
	errObj := out["error"].(map[string]any)
	if int(errObj["code"].(float64)) != codeMethodNotFound {
		t.Fatalf("未知方法应返回 method not found: %+v", errObj)
	}
}

func TestNotificationReturnsNil(t *testing.T) {
	s, _ := newTestServer(t)
	if resp := s.Handle(context.Background(), []byte(`{"jsonrpc":"2.0","method":"notifications/initialized"}`), admin()); resp != nil {
		t.Fatal("通知类消息不应产生响应")
	}
}

func TestPing(t *testing.T) {
	s, _ := newTestServer(t)
	out := call(t, s, `{"jsonrpc":"2.0","id":9,"method":"ping"}`, admin())
	if out["result"] == nil {
		t.Fatal("ping 应有 result")
	}
}
