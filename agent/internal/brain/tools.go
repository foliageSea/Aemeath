package brain

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"unicode/utf8"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

type deviceTool struct {
	info *schema.ToolInfo
	run  func(context.Context, string) (string, error)
}

func (t *deviceTool) Info(context.Context) (*schema.ToolInfo, error) { return t.info, nil }
func (t *deviceTool) InvokableRun(ctx context.Context, args string, _ ...tool.Option) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if len(args) > 4096 {
		return "", errors.New("tool arguments too large")
	}
	return t.run(ctx, args)
}

func deviceTools(host Host) []tool.BaseTool {
	makeTool := func(name, desc string, params map[string]*schema.ParameterInfo, run func(context.Context, string) (string, error)) tool.BaseTool {
		return &deviceTool{info: &schema.ToolInfo{Name: name, Desc: desc, ParamsOneOf: schema.NewParamsOneOfByParams(params)}, run: run}
	}
	noArgs := map[string]*schema.ParameterInfo{}
	return []tool.BaseTool{
		makeTool("lock_computer", "锁定运行 Agent 的电脑当前登录会话并保留应用。仅当用户明确要求电脑锁屏时调用。", noArgs, func(ctx context.Context, args string) (string, error) {
			if err := decodeArgs(args, &struct{}{}); err != nil {
				return "", err
			}
			err := host.LockComputer(ctx)
			if ctx.Err() != nil {
				return "", ctx.Err()
			}
			if err != nil {
				return `{"ok":false,"message":"电脑锁屏请求未能提交，请检查系统支持和权限；电脑锁屏状态尚未确认。"}`, nil
			}
			return `{"ok":true,"message":"已向操作系统提交本机锁屏请求；没有检测最终锁屏状态。"}`, nil
		}),
		makeTool("get_metrics", "读取电脑当前系统指标；用户询问 CPU、内存、网络等状态时使用。", noArgs, func(ctx context.Context, args string) (string, error) {
			if err := decodeArgs(args, &struct{}{}); err != nil {
				return "", err
			}
			return boundedToolText(host.Metrics()), nil
		}),
		makeTool("get_quota", "读取当前模型服务额度和用量。", noArgs, func(ctx context.Context, args string) (string, error) {
			if err := decodeArgs(args, &struct{}{}); err != nil {
				return "", err
			}
			return boundedToolText(host.Quota()), nil
		}),
		makeTool("set_page", "按用户请求切换设备页面：metrics 系统指标、quota 额度、voice 语音助手。", map[string]*schema.ParameterInfo{"page": {Type: schema.String, Required: true, Enum: []string{"metrics", "quota", "voice"}}}, func(ctx context.Context, args string) (string, error) {
			var input struct {
				Page string `json:"page"`
			}
			if err := decodeArgs(args, &input); err != nil {
				return "", err
			}
			switch input.Page {
			case "metrics", "quota", "voice":
			default:
				return "", errors.New("invalid device page")
			}
			if err := host.SetPage(ctx, input.Page); err != nil {
				return "", errors.New("device page command failed")
			}
			return "切页指令已发送；协议没有当前页面回执，不能确认设备实际已切换。", nil
		}),
		makeTool("set_screen", "按用户请求开关 ESP32 监控台的屏幕并设置 0 到 100 的亮度。", map[string]*schema.ParameterInfo{"on": {Type: schema.Boolean, Required: true}, "brightness": {Type: schema.Integer, Required: true, Desc: "屏幕亮度，0 到 100"}}, func(ctx context.Context, args string) (string, error) {
			var input struct {
				On         *bool `json:"on"`
				Brightness *int  `json:"brightness"`
			}
			if err := decodeArgs(args, &input); err != nil {
				return "", err
			}
			if input.On == nil || input.Brightness == nil || *input.Brightness < 0 || *input.Brightness > 100 {
				return "", errors.New("on and brightness (0..100) required")
			}
			if err := host.SetScreen(ctx, *input.On, *input.Brightness); err != nil {
				return "", errors.New("device screen command failed")
			}
			return "屏幕设置指令已发送；最终屏幕状态以设备后续上报为准。", nil
		}),
	}
}

func decodeArgs(args string, dst any) error {
	args = strings.TrimSpace(args)
	if len(args) < 2 || args[0] != '{' || args[len(args)-1] != '}' {
		return errors.New("tool expects a JSON object")
	}
	dec := json.NewDecoder(strings.NewReader(args))
	dec.DisallowUnknownFields()
	if dec.Decode(dst) != nil {
		return errors.New("invalid tool arguments")
	}
	var extra any
	if dec.Decode(&extra) != io.EOF {
		return errors.New("invalid trailing tool arguments")
	}
	return nil
}
func boundedToolText(s string) string {
	if len(s) <= maxTextBytes {
		return s
	}
	s = s[:maxTextBytes]
	for !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	return s
}
