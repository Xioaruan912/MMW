package traffic

import (
	"strings"
	"testing"
)

// xray api statsquery 的真实文本输出形态（节选）。
const textOutput = `stat: <
  name: "user>>>alice>>>traffic>>>uplink"
  value: 1024
>
stat: <
  name: "user>>>alice>>>traffic>>>downlink"
  value: 20480
>
stat: <
  name: "user>>>bob>>>traffic>>>uplink"
  value: 7
>
stat: <
  name: "inbound>>>in-443>>>traffic>>>uplink"
  value: 999999
>
stat: <
  name: "outbound>>>direct>>>traffic>>>downlink"
  value: 555
>
`

func TestParseStatsOutputText(t *testing.T) {
	got := ParseStatsOutput(textOutput)
	if len(got) != 2 {
		t.Fatalf("应解析出 2 个用户，得到 %d: %+v", len(got), got)
	}
	byEmail := map[string]UserUsage{}
	for _, u := range got {
		byEmail[u.Email] = u
	}
	alice, ok := byEmail["alice"]
	if !ok {
		t.Fatal("缺少 alice")
	}
	if alice.Up != 1024 || alice.Down != 20480 {
		t.Fatalf("alice 用量错误: %+v", alice)
	}
	bob, ok := byEmail["bob"]
	if !ok {
		t.Fatal("缺少 bob")
	}
	if bob.Up != 7 || bob.Down != 0 {
		t.Fatalf("bob 用量错误: %+v", bob)
	}
	// inbound / outbound 维度必须被忽略
	for _, u := range got {
		if strings.Contains(u.Email, ">>>") {
			t.Fatalf("非用户维度混入结果: %+v", u)
		}
	}
}

func TestParseStatsOutputJSON(t *testing.T) {
	// 新版 CLI 支持 --output json；解析器用同一套正则也应能兜住
	jsonOut := `{"stat":[
      {"name":"user>>>carol>>>traffic>>>uplink","value":"300"},
      {"name":"user>>>carol>>>traffic>>>downlink","value":"400"}
    ]}`
	got := ParseStatsOutput(jsonOut)
	if len(got) != 1 {
		t.Fatalf("应解析出 1 个用户，得到 %d", len(got))
	}
	if got[0].Email != "carol" || got[0].Up != 300 || got[0].Down != 400 {
		t.Fatalf("解析结果错误: %+v", got[0])
	}
}

func TestParseStatsOutputEmptyAndGarbage(t *testing.T) {
	cases := []string{"", "\n\n", "no stats here", `{"stat":[]}`}
	for _, c := range cases {
		if got := ParseStatsOutput(c); len(got) != 0 {
			t.Fatalf("输入 %q 应得到空结果，实得 %+v", c, got)
		}
	}
}

func TestParseStatsOutputAggregatesRepeats(t *testing.T) {
	// 同一 email 出现多次（例如同一用户挂在两个入站上）应当累加
	out := `stat: <
  name: "user>>>dave>>>traffic>>>uplink"
  value: 10
>
stat: <
  name: "user>>>dave>>>traffic>>>uplink"
  value: 20
>
stat: <
  name: "user>>>dave>>>traffic>>>downlink"
  value: 5
>
`
	got := ParseStatsOutput(out)
	if len(got) != 1 {
		t.Fatalf("应聚合为 1 条，得到 %d", len(got))
	}
	if got[0].Up != 30 || got[0].Down != 5 {
		t.Fatalf("累加结果错误: %+v", got[0])
	}
}

func TestParseStatsOutputHandlesZeroAndLarge(t *testing.T) {
	out := `name: "user>>>eve>>>traffic>>>uplink"
value: 0
name: "user>>>eve>>>traffic>>>downlink"
value: 9223372036854775807
`
	got := ParseStatsOutput(out)
	if len(got) != 1 {
		t.Fatalf("应解析出 1 条，得到 %d", len(got))
	}
	if got[0].Up != 0 {
		t.Fatalf("零值解析错误: %+v", got[0])
	}
	if got[0].Down != 9223372036854775807 {
		t.Fatalf("int64 边界解析错误: %d", got[0].Down)
	}
}
