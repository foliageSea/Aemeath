package brain

import (
	"errors"
	"strings"
)

// NormalizeReasoningEffort keeps an empty value to use the provider default.
func NormalizeReasoningEffort(value string) (string, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	switch value {
	case "", "low", "medium", "high":
		return value, nil
	default:
		return "", errors.New("思考强度须为服务端默认、low、medium 或 high")
	}
}
