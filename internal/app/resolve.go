package app

import (
	"fmt"
	"sort"
	"strings"

	"github.com/ywxzm03/Rivo/internal/llm"
)

// ApplyOverrides 把 CLI 参数（--provider/--model/--thinking）覆盖到配置上，
// 语义对齐 pi 的 resolveCliModel：--provider 必须与 --model 同给；
// --thinking 校验七档等级；--model 支持 provider/model、精确/子串匹配与 :level 后缀
func ApplyOverrides(cfg *Config, providerName, modelPattern, thinking string) error {
	// 1.思考等级：CLI 显式指定优先于 pattern 后缀与 settings 默认
	if thinking != "" {
		if !knownThinkingLevel(thinking) {
			return fmt.Errorf("unknown thinking level %q (supported: off, minimal, low, medium, high, xhigh, max)", thinking)
		}
		cfg.ThinkingLevel = llm.ThinkingLevel(thinking)
	}
	if providerName != "" && modelPattern == "" {
		return fmt.Errorf("--provider requires --model")
	}
	if modelPattern == "" {
		return nil
	}
	// 2.pattern 的 :level 后缀：合法等级直接作为思考等级（CLI --thinking 已设时忽略）
	pattern := modelPattern
	if i := strings.LastIndex(pattern, ":"); i >= 0 {
		suffix := pattern[i+1:]
		if knownThinkingLevel(suffix) {
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
		pattern = stripProviderPrefix(pattern, spec.id)
	} else if i := strings.Index(modelPattern, "/"); i >= 0 {
		if s, err := cfg.lookupProvider(modelPattern[:i]); err == nil {
			spec = s
			pattern = modelPattern[i+1:]
		}
	}
	// 4.无 provider：先跨全注册表精确匹配（id 或 provider/id）
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
	// 5.域内子串匹配：多个命中按 id 字典序取「最新」（对齐 pi 1.A 决策）
	model, err := resolveSubstring(cfg, spec, pattern)
	if err != nil {
		return err
	}
	if model == nil {
		return fmt.Errorf("model %q not found", modelPattern)
	}
	cfg.ProviderID, cfg.ModelID = model.Provider, model.ID
	return nil
}

// stripProviderPrefix 去掉 pattern 中与 provider id 同名的前缀（大小写不敏感）
func stripProviderPrefix(pattern, providerID string) string {
	prefix := providerID + "/"
	if len(pattern) > len(prefix) && strings.EqualFold(pattern[:len(prefix)], prefix) {
		return pattern[len(prefix):]
	}
	return pattern
}

// resolveExactAcrossProviders 跨全注册表精确匹配 id 或 provider/id；
// 无命中返回 (nil, nil)，跨 provider 歧义时返回错误（对齐 pi）
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

// resolveSubstring 在 provider 域内（未指定 provider 时为全域）按子串匹配模型，
// 多个命中按 id 字典序取最大（对齐 pi 的「取最新」策略）
func resolveSubstring(cfg *Config, spec providerSpec, pattern string) (*llm.Model, error) {
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
		return nil, nil
	}
	sort.Slice(matches, func(i, j int) bool { return matches[i].ID > matches[j].ID })
	return &matches[0], nil
}
