package responses

import (
	"strconv"
	"strings"

	"github.com/ywxzm03/Rivo/internal/llm"
)

// supportsPromptCacheOptions 判断模型是否支持 GPT-5.6+ 的 prompt_cache_options
func supportsPromptCacheOptions(model llm.Model) bool {
	if model.Provider != "openai" || model.API != llm.APIOpenAIResponses {
		return false
	}
	major, minor, ok := gptVersion(model.ID)
	return ok && (major > 5 || major == 5 && minor >= 6)
}

// gptVersion 解析 gpt-主版本.次版本及其后缀
func gptVersion(id string) (major, minor int, ok bool) {
	rest, ok := strings.CutPrefix(id, "gpt-")
	if !ok {
		return 0, 0, false
	}
	majorText, rest, _ := strings.Cut(rest, ".")
	major, ok = leadingInt(majorText)
	if !ok {
		return 0, 0, false
	}
	if rest == "" {
		return major, 0, true
	}
	minor, _ = leadingInt(rest)
	return major, minor, true
}

func leadingInt(value string) (int, bool) {
	end := 0
	for end < len(value) && value[end] >= '0' && value[end] <= '9' {
		end++
	}
	if end == 0 {
		return 0, false
	}
	parsed, err := strconv.Atoi(value[:end])
	return parsed, err == nil
}
