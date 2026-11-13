package brain

import (
	"fmt"
	"time"
)

// timeSnapshotPrompt is sampled once per user turn, not once per model request.
// It requires no network and is never appended to conversation history.
func timeSnapshotPrompt(now time.Time) string {
	weekdays := [...]string{"星期日", "星期一", "星期二", "星期三", "星期四", "星期五", "星期六"}
	zone, _ := now.Zone()
	location := now.Location().String()
	if zone == "" {
		zone = "UTC" + now.Format("-07:00")
	}
	if location == "" {
		location = zone
	}
	return fmt.Sprintf("\n\n本轮时间快照：\n- 本地时间：%s\n- 时区：%s（%s，UTC%s）\n- 星期：%s\n- UTC时间：%s\n- 来源：电脑系统时钟，未进行外部校时。\n普通本地时间、日期、星期及今天/明天/今年等相对日期问题，直接使用此快照，不要为确认当前时间而调用工具或搜索；不要使用训练知识或旧轮次时间替代它。同一轮以此采样时刻为稳定基准，它不是持续更新的时钟；下一轮会重新采样。当前没有独立的时间查询工具。仅凭此快照不得声称已联网校时，也不会因此创建提醒或定时任务。",
		now.Format(time.RFC3339), location, zone, now.Format("-07:00"), weekdays[now.Weekday()], now.UTC().Format(time.RFC3339))
}
