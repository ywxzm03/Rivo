package shell

import (
	"fmt"
	"math"
	"strconv"
	"time"
)

// maxTimeoutMS 是 timeout 参数的上限，避免超出 time.Duration 可表示范围
const maxTimeoutMS = 2_147_483_647

// ParseArgs 解析 shell 类工具共用的 command / timeout 参数。
// timeout 缺省表示不限时，出现时必须为有限正数秒
func ParseArgs(args map[string]any, toolName string) (string, time.Duration, error) {
	command, ok := args["command"].(string)
	if !ok || command == "" {
		return "", 0, fmt.Errorf("%s: command must be a non-empty string", toolName)
	}
	v, exists := args["timeout"]
	if !exists {
		return command, 0, nil
	}
	seconds, ok := v.(float64)
	if !ok || math.IsNaN(seconds) || math.IsInf(seconds, 0) || seconds <= 0 {
		return "", 0, fmt.Errorf("%s: invalid timeout: must be a finite number of seconds greater than 0", toolName)
	}
	if seconds*1000 > maxTimeoutMS {
		return "", 0, fmt.Errorf("%s: invalid timeout: maximum is %s seconds", toolName, FormatTimeout(float64(maxTimeoutMS)/1000))
	}
	return command, time.Duration(seconds * float64(time.Second)), nil
}

// FormatTimeout 去掉多余的小数零，供错误与说明文案使用
func FormatTimeout(seconds float64) string {
	return strconv.FormatFloat(seconds, 'f', -1, 64)
}
