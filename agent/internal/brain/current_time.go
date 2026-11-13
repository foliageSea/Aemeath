package brain

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"aemeath/agent/internal/timeaware"
)

// CurrentTimeReport is retained for displaying time cards in saved conversations.
type CurrentTimeReport struct {
	OK      bool           `json:"ok"`
	Info    timeaware.Info `json:"info"`
	Message string         `json:"message,omitempty"`
}

// normalizeTimeZone supports arguments displayed in historical time cards.
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
