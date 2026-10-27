package provider

import (
	"fmt"
	"sort"
	"strings"

	"github.com/ywxzm03/Rivo/internal/llm"
)

// ApplyOverrides 把 CLI 参数覆盖到配置上：--provider 必须与 --model 同给，
// --effort 校验七档等级，--model 支持 provider/model、精确/子串匹配与 :level 后缀
func ApplyOverrides(cfg *Config, providerName, modelPattern, thinking string) error {
	// 1.思考等级：CLI 显式指定优先于 pattern 后缀与 settings 默认
	if thinking != "" {
		if !llm.ValidThinkingLevel(llm.ThinkingLevel(thinking)) {
			return fmt.Errorf("unknown effort level %q (supported: %s)", thinking, strings.Join(llm.ThinkingLevelNames(), ", "))
		}
		cfg.ThinkingLevel = llm.ThinkingLevel(thinking)
	}
	if providerName != "" && modelPattern == "" {
		return fmt.Errorf("--provider requires --model")
	}
	if modelPattern == "" {
		return nil
	}
	// 2.pattern 的 :level 后缀：合法等级直接作为思考等级
	pattern := modelPattern
	if i := strings.LastIndex(pattern, ":"); i >= 0 {
		suffix := pattern[i+1:]
		if llm.ValidThinkingLevel(llm.ThinkingLevel(suffix)) {
			pattern = pattern[:i]
			if thinking == "" {
				cfg.ThinkingLevel = llm.ThinkingLevel(suffix)
			}
		}
	}
	// 3.provider 解析：--provider 显式指定，或 pattern 的斜杠前缀推断
	var spec providerSpec
	if providerName != "" {
		s, err := cfg.lookupProvider(providerName)
		if err != nil {
			return err
		}
		spec = s
		var stripped bool
		pattern, stripped = stripProviderPrefix(pattern, spec.id)
		if stripped && pattern == "" {
			return fmt.Errorf("model %q is missing a model name after the provider prefix", modelPattern)
		}
	} else if i := strings.Index(pattern, "/"); i >= 0 {
		if s, err := cfg.lookupProvider(pattern[:i]); err == nil {
			spec = s
			pattern = pattern[i+1:]
			if pattern == "" {
				return fmt.Errorf("model %q is missing a model name after the provider prefix", modelPattern)
			}
		}
	}
	// 4.无 provider：先跨全注册表精确匹配
	if spec.id == "" {
		model, err := resolveExactAcrossProviders(cfg, pattern)
		if err != nil {
			return err
		}
		if model != nil {
			cfg.ProviderID, cfg.ModelID = model.Provider, model.ID
			return nil
		}
	}
	// 5.域内子串匹配：多个命中按 id 字典序取「最新」
	model := resolveSubstring(cfg, spec, pattern)
	if model == nil {
		return fmt.Errorf("model %q not found", modelPattern)
	}
	cfg.ProviderID, cfg.ModelID = model.Provider, model.ID
	return nil
}

// stripProviderPrefix 去掉 pattern 中与 provider id 同名的前缀，返回剩余部分与是否命中
func stripProviderPrefix(pattern, providerID string) (string, bool) {
	prefix := providerID + "/"
	if len(pattern) >= len(prefix) && strings.EqualFold(pattern[:len(prefix)], prefix) {
		return pattern[len(prefix):], true
	}
	return pattern, false
}

// resolveExactAcrossProviders 跨全注册表精确匹配 id 或 provider/id；
// 无命中返回 (nil, nil)，歧义时返回错误
func resolveExactAcrossProviders(cfg *Config, pattern string) (*llm.Model, error) {
	lower := strings.ToLower(pattern)
	var matches []llm.Model
	for _, spec := range cfg.registry {
		for _, m := range spec.models {
			if strings.ToLower(m.ID) == lower || strings.ToLower(m.Provider+"/"+m.ID) == lower {
				matches = append(matches, m)
			}
		}
	}
	switch len(matches) {
	case 0:
		return nil, nil
	case 1:
		return &matches[0], nil
	default:
		names := make([]string, 0, len(matches))
		for _, m := range matches {
			names = append(names, m.Provider+"/"+m.ID)
		}
		sort.Strings(names)
		return nil, fmt.Errorf("model %q is ambiguous across providers: %s. Use --provider or provider/model", pattern, strings.Join(names, ", "))
	}
}

// resolveSubstring 在 provider 域内（未指定时为全域）按子串匹配模型，多个命中取 id 最大者
func resolveSubstring(cfg *Config, spec providerSpec, pattern string) *llm.Model {
	var candidates []llm.Model
	if spec.id != "" {
		candidates = spec.models
	} else {
		for _, s := range cfg.registry {
			candidates = append(candidates, s.models...)
		}
	}
	lower := strings.ToLower(pattern)
	var matches []llm.Model
	for _, m := range candidates {
		if strings.Contains(strings.ToLower(m.ID), lower) || strings.Contains(strings.ToLower(m.Name), lower) {
			matches = append(matches, m)
		}
	}
	if len(matches) == 0 {
		return nil
	}
	sort.Slice(matches, func(i, j int) bool { return matches[i].ID > matches[j].ID })
	return &matches[0]
}
