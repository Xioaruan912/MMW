package agentproto

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestEnvelopeRoundTrip(t *testing.T) {
	env, err := NewEnvelope(TypeHeartbeat, HeartbeatPayload{
		UptimeSec:   120,
		XrayRunning: true,
		Seq:         7,
	})
	if err != nil {
		t.Fatalf("构造信封失败: %v", err)
	}
	if err := env.Validate(); err != nil {
		t.Fatalf("信封校验失败: %v", err)
	}

	raw, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("序列化失败: %v", err)
	}

	var got Envelope
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("反序列化失败: %v", err)
	}
	var hb HeartbeatPayload
	if err := got.Decode(&hb); err != nil {
		t.Fatalf("解载荷失败: %v", err)
	}
	if hb.Seq != 7 || !hb.XrayRunning || hb.UptimeSec != 120 {
		t.Fatalf("载荷不一致: %+v", hb)
	}
}

func TestEnvelopeRejectsWrongVersion(t *testing.T) {
	env := MustEnvelope(TypeAck, AckPayload{ID: "x"})
	env.V = 99
	if err := env.Validate(); err == nil {
		t.Fatal("版本不符时应当报错")
	}
}

func TestUnknownFieldTolerated(t *testing.T) {
	// 前向兼容：旧端解析带新字段的载荷不应失败
	raw := []byte(`{"uptime_sec":1,"xray_running":false,"seq":1,"future_field":"whatever"}`)
	var hb HeartbeatPayload
	if err := json.Unmarshal(raw, &hb); err != nil {
		t.Fatalf("未知字段导致解析失败: %v", err)
	}
	if hb.Seq != 1 {
		t.Fatalf("已识别字段解析错误: %+v", hb)
	}
}

func TestTaskSignAndVerify(t *testing.T) {
	pub, priv, err := GenerateMasterKey()
	if err != nil {
		t.Fatalf("生成密钥失败: %v", err)
	}
	nonce, err := NewNonce()
	if err != nil {
		t.Fatalf("生成 nonce 失败: %v", err)
	}
	task := &TaskPayload{
		TaskID: "tsk_01",
		Kind:   TaskXrayRestart,
		Nonce:  nonce,
		Args:   RawArgs{},
	}
	if err := SignTask(priv, task); err != nil {
		t.Fatalf("签名失败: %v", err)
	}
	if err := VerifyTask(pub, task); err != nil {
		t.Fatalf("验签失败: %v", err)
	}

	// 篡改参数后验签必须失败
	task.Args = RawArgs{"injected": true}
	if err := VerifyTask(pub, task); err == nil {
		t.Fatal("参数被篡改后验签应当失败")
	}
}

func TestVerifyTaskRejectsMissingSignature(t *testing.T) {
	pub, _, _ := GenerateMasterKey()
	task := &TaskPayload{TaskID: "tsk_02", Kind: TaskXrayStart, Args: RawArgs{}}
	if err := VerifyTask(pub, task); err == nil {
		t.Fatal("缺少签名时应当失败")
	}
}

func TestValidateTaskWhitelist(t *testing.T) {
	if err := ValidateTask("shell.exec", RawArgs{}); err == nil {
		t.Fatal("任意命令类任务必须被拒绝")
	}
	if err := ValidateTask(TaskXrayInstall, RawArgs{}); err == nil {
		t.Fatal("缺少必填参数时应当失败")
	}
	if err := ValidateTask(TaskXrayInstall, RawArgs{"mode": "weird"}); err == nil {
		t.Fatal("非法 mode 应当失败")
	}
	if err := ValidateTask(TaskXrayInstall, RawArgs{"mode": "embedded"}); err != nil {
		t.Fatalf("合法任务被拒: %v", err)
	}
	if err := ValidateTask(TaskXrayRestart, RawArgs{}); err != nil {
		t.Fatalf("无参任务被拒: %v", err)
	}
}

func TestRawArgsDigestStable(t *testing.T) {
	a := RawArgs{"mode": "embedded", "n": float64(1)}
	b := RawArgs{"n": float64(1), "mode": "embedded"}
	if a.Digest() != b.Digest() {
		t.Fatal("同一组参数（顺序不同）的摘要应当一致（json 编码按 key 排序）")
	}
	c := RawArgs{"mode": "external"}
	if a.Digest() == c.Digest() {
		t.Fatal("不同参数的摘要不应相同")
	}
}

func TestMasterKeyPersist(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/master.key"

	pub1, _, created, err := LoadOrCreateMasterKey(path)
	if err != nil {
		t.Fatalf("首次生成失败: %v", err)
	}
	if !created {
		t.Fatal("首次应当标记为新建")
	}

	pub2, _, created, err := LoadOrCreateMasterKey(path)
	if err != nil {
		t.Fatalf("二次加载失败: %v", err)
	}
	if created {
		t.Fatal("二次加载不应再新建")
	}
	if !pub1.Equal(pub2) {
		t.Fatal("两次加载得到的公钥应当一致")
	}

	// 往返编码
	enc := EncodePublicKey(pub1)
	dec, err := DecodePublicKey(enc)
	if err != nil || !dec.Equal(pub1) {
		t.Fatalf("公钥编解码不一致: %v", err)
	}
}

func TestTaskSigningInputFormat(t *testing.T) {
	in := string(TaskSigningInput("tsk_1", "xray.restart", "abc", "n1"))
	if in != "v1|tsk_1|xray.restart|abc|n1" {
		t.Fatalf("签名输入格式被改动: %q", in)
	}
	if !strings.HasPrefix(in, "v1|") {
		t.Fatal("签名输入必须以版本前缀开头")
	}
}
