package brain

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"aemeath/agent/internal/timeaware"
	"github.com/cloudwego/eino/schema"
)

const currentTimeInstructions = "普通本地时间、日期、星期和相对日期问题直接使用本轮时间快照，不要调用 get_current_time。仅在用户明确要求外部校时、需要其他时区的可靠换算，或长任务确实需要刷新当前时刻时调用 get_current_time；指定地区时传对应IANA时区。本轮已取得所需时区的可用工具结果后直接复用，以该结果为准，不要重复查询。网络时间不可用而回退系统时间也是可用结果，说明来源后继续回答，不要为了外部校时反复重试，不能声称已完成外部校时。时间工具只查询，不会修改系统时钟，也不会创建提醒或定时任务。"

type currentTimeFunc func(context.Context, string) (timeaware.Info, error)

func defaultCurrentTime(ctx context.Context, timezone string) (timeaware.Info, error) {
	return timeaware.New().Now(ctx, timezone)
}

type CurrentTimeReport struct {
	OK      bool           `json:"ok"`
	Info    timeaware.Info `json:"info"`
	Message string         `json:"message,omitempty"`
}

func normalizeTimeZone(value string) string {
	value = strings.TrimSpace(value)
	switch value {
	case "北京时间", "中国时间", "北京", "上海":
		return "Asia/Shanghai"
	case "东京时间", "日本时间", "东京":
		return "Asia/Tokyo"
	case "伦敦时间", "伦敦":
		return "Europe/London"
	case "纽约时间", "纽约":
		return "America/New_York"
	case "GMT", "格林尼治标准时间":
		return "UTC"
	default:
		return value
	}
}

func currentTimeTool(now currentTimeFunc) *deviceTool {
	if now == nil {
		now = defaultCurrentTime
	}
	return &deviceTool{
		info: &schema.ToolInfo{Name: "get_current_time", Desc: "按需查询外部时间，返回日期、星期、时区、UTC与Unix时间。普通本地时间、日期和星期直接使用本轮时间快照，不调用此工具。仅用于明确要求外部校时、其他时区可靠换算或长任务刷新；支持IANA时区，外部服务失败会明确标注系统时间回退，得到可用结果后不要重复查询。不会设置闹钟、提醒或修改系统时间。", ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"timezone": {Type: schema.String, Desc: "IANA时区，如Asia/Shanghai、Asia/Tokyo、Europe/London、America/New_York；省略取电脑本机时区"},
		})},
		run: func(ctx context.Context, args string) (string, error) {
			var input struct {
				Timezone string `json:"timezone"`
			}
			if decodeArgs(args, &input) != nil {
				return currentTimeFailure("时间参数无效，请使用可选的 timezone 字符串。"), nil
			}
			input.Timezone = normalizeTimeZone(input.Timezone)
			if len(input.Timezone) > 128 || !utf8.ValidString(input.Timezone) {
				return currentTimeFailure("时区名称无效，请使用 Asia/Shanghai 等 IANA 时区。"), nil
			}
			if input.Timezone != "" && input.Timezone != "Local" {
				if _, err := time.LoadLocation(input.Timezone); err != nil {
					return currentTimeFailure("时区名称无效，请使用 Asia/Shanghai 等 IANA 时区。"), nil
				}
			}
			if err := ctx.Err(); err != nil {
				return "", err
			}
			info, err := now(ctx, input.Timezone)
			if ctx.Err() != nil {
				return "", ctx.Err()
			}
			if err != nil {
				return currentTimeFailure("当前时间查询失败，请稍后重试。"), nil
			}
			report := CurrentTimeReport{OK: true, Info: info}
			data, _ := json.Marshal(report)
			if _, ok := ParseCurrentTimeReport(string(data)); !ok {
				return currentTimeFailure("时间来源返回了无效结果，请稍后重试。"), nil
			}
			return string(data), nil
		},
	}
}

func currentTimeFailure(message string) string {
	data, _ := json.Marshal(CurrentTimeReport{OK: false, Message: message})
	return string(data)
}

func ParseCurrentTimeReport(raw string) (CurrentTimeReport, bool) {
	var report CurrentTimeReport
	if len(raw) > 4096 || !utf8.ValidString(raw) || json.Unmarshal([]byte(raw), &report) != nil || len(report.Message) > 512 {
		return CurrentTimeReport{}, false
	}
	if !report.OK {
		return report, report.Message != ""
	}
	info := report.Info
	local, err := time.Parse(time.RFC3339Nano, info.DateTime)
	utc, utcErr := time.Parse(time.RFC3339Nano, info.UTC)
	if err != nil || utcErr != nil || !local.Equal(utc) || local.Unix() != info.Unix || info.Date != local.Format("2006-01-02") || info.Time != local.Format("15:04:05") || info.UTCOffset != local.Format("-07:00") || len(info.Timezone) == 0 || len(info.Timezone) > 128 || len(info.Notice) > 256 {
		return CurrentTimeReport{}, false
	}
	if _, offset := utc.Zone(); offset != 0 {
		return CurrentTimeReport{}, false
	}
	weekdays := []string{"星期日", "星期一", "星期二", "星期三", "星期四", "星期五", "星期六"}
	if info.Weekday != weekdays[local.Weekday()] {
		return CurrentTimeReport{}, false
	}
	switch info.Source {
	case "network":
		if info.Provider != "timeapi.io" {
			return CurrentTimeReport{}, false
		}
	case "system_fallback":
		if info.Provider != "system" || info.Notice == "" {
			return CurrentTimeReport{}, false
		}
	default:
		return CurrentTimeReport{}, false
	}
	return report, true
}

func (report CurrentTimeReport) Summary() string {
	if !report.OK {
		return report.Message
	}
	return fmt.Sprintf("%s %s · %s · %s", report.Info.Date, report.Info.Time, report.Info.Weekday, report.Info.Timezone)
}
