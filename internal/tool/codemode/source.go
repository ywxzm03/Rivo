package codemode

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"
)

// parseSource 只解析首行选项，保留原始行数供错误位置使用
func parseSource(code string) (string, SourceOptions, error) {
	var options SourceOptions
	trimmed := strings.TrimSpace(code)
	if trimmed == "" {
		return "", options, fmt.Errorf("codemode: code must not be empty")
	}
	if strings.HasPrefix(trimmed, "```") {
		return "", options, fmt.Errorf("codemode: code must not use Markdown fences")
	}
	for index, line := range strings.Split(code, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "// @options") {
			continue
		}
		if index != 0 {
			return "", options, fmt.Errorf("codemode: @options must be on the first line")
		}
		payload, ok := strings.CutPrefix(line, "// @options:")
		if !ok {
			return "", options, fmt.Errorf("codemode: expected // @options: followed by a JSON object")
		}
		payload = strings.TrimSpace(payload)
		if !strings.HasPrefix(payload, "{") {
			return "", options, fmt.Errorf("codemode: @options must be a JSON object")
		}
		decoder := json.NewDecoder(strings.NewReader(payload))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&options); err != nil {
			return "", options, fmt.Errorf("codemode: @options: %w", err)
		}
		var extra json.RawMessage
		if err := decoder.Decode(&extra); err != io.EOF {
			return "", options, fmt.Errorf("codemode: @options must contain one JSON object")
		}
	}
	if options.MaxOutputTokens != nil && *options.MaxOutputTokens < 0 {
		return "", options, fmt.Errorf("codemode: max_output_tokens must be a nonnegative integer")
	}
	if options.TimeoutMS != nil && (*options.TimeoutMS <= 0 || *options.TimeoutMS > int64((1<<63-1)/time.Millisecond)) {
		return "", options, fmt.Errorf("codemode: timeout_ms must be positive milliseconds within time.Duration range")
	}
	return code, options, nil
}

func timeoutDuration(milliseconds int64) time.Duration {
	return time.Duration(milliseconds) * time.Millisecond
}
