// Package args 提供工具参数的取值与校验，
// 让各工具的「缺参数」错误文案只有一处定义
package args

import (
	"encoding/json"
	"fmt"
	"strings"
)

// RequiredString 取必填字符串参数，缺失或只有空白时报错
func RequiredString(values map[string]any, tool, key string) (string, error) {
	value, ok := values[key].(string)
	if !ok || strings.TrimSpace(value) == "" {
		return "", fmt.Errorf("%s: missing required parameter: %s", tool, key)
	}
	return value, nil
}

// Decode 把工具参数解成强类型 Params struct，
// 让每个工具只声明一次自己的参数形状
func Decode[T any](values map[string]any) (T, error) {
	var params T
	raw, err := json.Marshal(values)
	if err != nil {
		return params, fmt.Errorf("encode args: %w", err)
	}
	if err := json.Unmarshal(raw, &params); err != nil {
		return params, fmt.Errorf("decode args: %w", err)
	}
	return params, nil
}
