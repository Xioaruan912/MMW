package agentproto

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// RawArgs 承载任务参数：固定字段用强类型，扩展字段保持可读。
type RawArgs map[string]any

// GetString 取字符串参数。
func (a RawArgs) GetString(key string) string {
	if a == nil {
		return ""
	}
	v, ok := a[key]
	if !ok {
		return ""
	}
	s, _ := v.(string)
	return s
}

// GetInt 取整型参数（JSON 数字解码为 float64）。
func (a RawArgs) GetInt(key string) int64 {
	if a == nil {
		return 0
	}
	switch v := a[key].(type) {
	case float64:
		return int64(v)
	case int64:
		return v
	case int:
		return int64(v)
	}
	return 0
}

// GetBool 取布尔参数。
func (a RawArgs) GetBool(key string) bool {
	if a == nil {
		return false
	}
	b, _ := a[key].(bool)
	return b
}

// GetMap 取嵌套对象。
func (a RawArgs) GetMap(key string) map[string]any {
	if a == nil {
		return nil
	}
	m, _ := a[key].(map[string]any)
	return m
}

// Digest 返回参数的稳定摘要，用于签名。
//
// 重要：nil 与空 map 必须产生**相同**摘要。否则主控侧 `RawArgs{}` 与 Agent 侧解码得到的
// `nil`（例如 `args` 字段被 JSON 省略时）会算出不同摘要，导致签名校验失败。
func (a RawArgs) Digest() string {
	src := a
	if src == nil {
		src = RawArgs{}
	}
	b, err := json.Marshal(src)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// ---------------------------------------------------------------------------
// 任务白名单
// ---------------------------------------------------------------------------

// 任务类型常量。**没有任何"执行任意命令"的类型**，这是有意的安全边界。
const (
	TaskXrayInstall     = "xray.install"
	TaskXrayApplyConfig = "xray.apply_config"
	TaskXrayStart       = "xray.start"
	TaskXrayStop        = "xray.stop"
	TaskXrayRestart     = "xray.restart"
	TaskXrayUninstall   = "xray.uninstall"

	TaskNginxInstall   = "nginx.install"
	TaskNginxUninstall = "nginx.uninstall"

	TaskAgentRotateToken = "agent.rotate_token"
	TaskAgentUpgrade     = "agent.upgrade"

	TaskSysReboot = "sys.reboot"

	TaskACMESDeployCert = "acme.deploy_cert"
	TaskProbeCollectNow = "probe.collect_now"
	TaskXrayQueryStats  = "xray.query_stats"
	TaskProbeTCPPing    = "probe.tcp_ping"
	TaskSpeedtestRun    = "speedtest.run"
)

// taskSpec 描述一个任务的参数约束。
type taskSpec struct {
	Required  []string      // 必填参数
	Timeout   time.Duration // 建议超时（Agent 侧用于 deadline 校验）
	AdminOnly bool          // 是否要求管理员二次确认
}

// taskSpecs 是任务白名单表。新增任务必须同时在这里登记，否则一律拒绝。
var taskSpecs = map[string]taskSpec{
	TaskXrayInstall:      {Required: []string{"mode"}, Timeout: 10 * time.Minute},
	TaskXrayApplyConfig:  {Required: []string{"config", "rev"}, Timeout: 60 * time.Second},
	TaskXrayStart:        {Timeout: 30 * time.Second},
	TaskXrayStop:         {Timeout: 30 * time.Second},
	TaskXrayRestart:      {Timeout: 30 * time.Second},
	TaskXrayUninstall:    {Timeout: 2 * time.Minute},
	TaskNginxInstall:     {Timeout: 5 * time.Minute},
	TaskNginxUninstall:   {Timeout: 2 * time.Minute},
	TaskAgentRotateToken: {Required: []string{"new_token"}, Timeout: 30 * time.Second},
	TaskAgentUpgrade:     {Required: []string{"version", "sha256", "url"}, Timeout: 5 * time.Minute},
	TaskSysReboot:        {Timeout: 30 * time.Second, AdminOnly: true},
	TaskACMESDeployCert:  {Required: []string{"domain", "cert_pem", "key_pem"}, Timeout: 60 * time.Second},
	TaskProbeCollectNow:  {Timeout: 20 * time.Second},
	TaskXrayQueryStats:   {Timeout: 30 * time.Second},
	TaskProbeTCPPing:     {Required: []string{"targets"}, Timeout: 60 * time.Second},
	TaskSpeedtestRun:     {Required: []string{"target"}, Timeout: 3 * time.Minute},
}

// KnownTask 判断任务类型是否在白名单内。
func KnownTask(kind string) bool {
	_, ok := taskSpecs[kind]
	return ok
}

// TaskTimeout 返回该任务的建议超时。
func TaskTimeout(kind string) time.Duration {
	if spec, ok := taskSpecs[kind]; ok && spec.Timeout > 0 {
		return spec.Timeout
	}
	return 60 * time.Second
}

// TaskRequiresAdmin 表示该任务需要管理员权限发起。
func TaskRequiresAdmin(kind string) bool {
	spec, ok := taskSpecs[kind]
	return ok && spec.AdminOnly
}

// ValidateTask 校验任务类型与必填参数。
func ValidateTask(kind string, args RawArgs) error {
	spec, ok := taskSpecs[kind]
	if !ok {
		return fmt.Errorf("未知任务类型 %q（不在白名单内）", kind)
	}
	for _, req := range spec.Required {
		if _, exists := args[req]; !exists {
			return fmt.Errorf("任务 %s 缺少必填参数 %q", kind, req)
		}
	}
	switch kind {
	case TaskXrayInstall:
		switch args.GetString("mode") {
		case "embedded", "external":
		default:
			return errors.New("xray.install 的 mode 必须是 embedded 或 external")
		}
	case TaskAgentUpgrade:
		if args.GetString("url") == "" || args.GetString("sha256") == "" {
			return errors.New("agent.upgrade 需要 url 与 sha256")
		}
	}
	return nil
}

// TaskKinds 返回全部已知任务类型（供文档与前端使用）。
func TaskKinds() []string {
	out := make([]string, 0, len(taskSpecs))
	for k := range taskSpecs {
		out = append(out, k)
	}
	return out
}
