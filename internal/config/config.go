package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/ywxzm03/Rivo/internal/llm"
)

// APIOpenAIResponses 是 models.json 中 provider 支持的唯一协议标识
const APIOpenAIResponses = "openai-responses"
const APIOpenAICompletions = "openai-completions"

// agentDirEnv 是 agent 配置目录的环境变量覆盖名
const agentDirEnv = "RIVO_AGENT_DIR"

// Settings 对应 ~/.rivo/agent/settings.json；只消费默认模型相关字段，其余字段忽略
type Settings struct {
	DefaultProvider      string `json:"defaultProvider"`
	DefaultModel         string `json:"defaultModel"`
	DefaultThinkingLevel string `json:"defaultThinkingLevel"`
}

// Model 是 models.json 中 provider 下的模型条目
type Model struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	API           string `json:"api"`
	ContextWindow int    `json:"contextWindow"`
	MaxTokens     int    `json:"maxTokens"`
	// Reasoning 表示模型支持 provider reasoning 输出
	Reasoning bool `json:"reasoning"`
	// SupportsImages 表示模型接受图片输入，影响 read 工具是否把图片降级为文字说明
	SupportsImages bool `json:"supportsImages"`
	// Input 是 PI 配置中的模型输入能力列表；包含 image 时表示模型接受图片
	Input []string `json:"input"`
	// ThinkingLevelMap 把思考等级映射为该模型的 reasoning effort；值 null 表示该等级禁用
	ThinkingLevelMap map[string]*string `json:"thinkingLevelMap"`
	// SamplingParams 是并入请求体的自由采样参数（temperature/top_p 等）
	SamplingParams map[string]any `json:"samplingParams"`
	// Cost 是模型每百万 token 的美元费率；nil 表示未配置
	Cost *llm.CostRates `json:"cost"`

	contextWindowSet bool
}

// UnmarshalJSON 记录 contextWindow 是否在配置中显式出现
func (m *Model) UnmarshalJSON(data []byte) error {
	type plainModel Model
	var decoded plainModel
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	*m = Model(decoded)
	_, m.contextWindowSet = fields["contextWindow"]
	return nil
}

// HasContextWindow 表示 contextWindow 是否由配置显式指定
func (m Model) HasContextWindow() bool {
	return m.contextWindowSet || m.ContextWindow != 0
}

// CostOverride 是 modelOverrides 中可逐字段覆盖的费率
type CostOverride struct {
	Input      *float64 `json:"input"`
	Output     *float64 `json:"output"`
	CacheRead  *float64 `json:"cacheRead"`
	CacheWrite *float64 `json:"cacheWrite"`
}

// ModelOverride 是 models.json 中针对已有模型的字段级覆盖
type ModelOverride struct {
	Name             *string            `json:"name"`
	Reasoning        *bool              `json:"reasoning"`
	Input            []string           `json:"input"`
	Cost             *CostOverride      `json:"cost"`
	ContextWindow    *int               `json:"contextWindow"`
	MaxTokens        *int               `json:"maxTokens"`
	ThinkingLevelMap map[string]*string `json:"thinkingLevelMap"`
	SamplingParams   map[string]any     `json:"samplingParams"`
}

// Provider 是 models.json 中 providers 下的一个自定义 provider 条目
// （pi 的 schema：ModelsConfigSchema = { providers: Record<string, ProviderConfigSchema> }）
type Provider struct {
	Name    string  `json:"name"`
	BaseURL string  `json:"baseUrl"`
	API     string  `json:"api"`
	APIKey  string  `json:"apiKey"`
	Models  []Model `json:"models"`
	// ModelOverrides 覆盖 provider 中已有模型的元数据
	ModelOverrides map[string]ModelOverride `json:"modelOverrides"`

	apiSet       bool
	apiDefaulted bool
}

// UnmarshalJSON 记录 provider 是否显式指定 api
func (p *Provider) UnmarshalJSON(data []byte) error {
	type plainProvider Provider
	var decoded plainProvider
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	*p = Provider(decoded)
	_, p.apiSet = fields["api"]
	return nil
}

// HasAPI 表示 provider 是否由配置显式指定 api
func (p Provider) HasAPI() bool {
	return p.apiSet || (p.API != "" && !p.apiDefaulted)
}

// ModelsFile 对应 ~/.rivo/agent/models.json：keyed by provider id，解析时保持文件顺序
type ModelsFile struct {
	Providers []NamedProvider
}

// NamedProvider 是带 id 的 provider 条目，顺序与文件一致
type NamedProvider struct {
	ID       string
	Provider Provider
}

// Dir 返回 agent 配置目录 ~/.rivo/agent；设置 RIVO_AGENT_DIR 环境变量时使用其值
// （对齐 pi 的 PI_CODING_AGENT_DIR），否则回落到用户主目录
func Dir() (string, error) {
	if dir := strings.TrimSpace(os.Getenv(agentDirEnv)); dir != "" {
		return dir, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("config: failed to get user home directory: %w", err)
	}
	return filepath.Join(home, ".rivo", "agent"), nil
}

// LoadSettings 读取 settings.json；文件不存在返回零值配置
func LoadSettings() (Settings, error) {
	dir, err := Dir()
	if err != nil {
		return Settings{}, err
	}
	return loadSettingsFromFile(filepath.Join(dir, "settings.json"))
}

func loadSettingsFromFile(path string) (Settings, error) {
	var s Settings
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return s, nil
		}
		return Settings{}, fmt.Errorf("config: failed to read settings file: %w", err)
	}
	if err := json.Unmarshal(data, &s); err != nil {
		return Settings{}, fmt.Errorf("config: invalid settings file: %w", err)
	}
	return s, nil
}

// LoadModels 读取 models.json；文件不存在返回空注册表
func LoadModels() (ModelsFile, error) {
	dir, err := Dir()
	if err != nil {
		return ModelsFile{}, err
	}
	return loadModelsFromFile(filepath.Join(dir, "models.json"))
}

func loadModelsFromFile(path string) (ModelsFile, error) {
	var m ModelsFile
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return m, nil
		}
		return ModelsFile{}, fmt.Errorf("config: failed to read models file: %w", err)
	}
	if err := m.UnmarshalJSON(data); err != nil {
		return ModelsFile{}, fmt.Errorf("config: invalid models file: %w", err)
	}
	if err := m.validate(); err != nil {
		return ModelsFile{}, err
	}
	// 1.凭据支持 $ENV/${ENV} 插值与明文，插值在读取后统一执行
	for i := range m.Providers {
		m.Providers[i].Provider.APIKey = interpolate(m.Providers[i].Provider.APIKey)
	}
	return m, nil
}

// UnmarshalJSON 解析 {"providers": {...}} 结构并保持文件中 provider 的出现顺序
// （schema 对齐 pi 的 ModelsConfigSchema：顶层必须有 providers 对象）
func (m *ModelsFile) UnmarshalJSON(data []byte) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	if delim, ok := tok.(json.Delim); !ok || delim != '{' {
		return fmt.Errorf("config: models file must be a JSON object")
	}
	seen := false
	for dec.More() {
		keyTok, err := dec.Token()
		if err != nil {
			return err
		}
		key, ok := keyTok.(string)
		if !ok {
			return fmt.Errorf("config: models file keys must be strings")
		}
		if key != "providers" {
			// 忽略未知顶层字段，与 pi 的 TypeBox 校验不冲突；providers 必须存在
			var skip json.RawMessage
			if err := dec.Decode(&skip); err != nil {
				return err
			}
			continue
		}
		if seen {
			return fmt.Errorf("config: models file has duplicate \"providers\" key")
		}
		seen = true
		if err := m.decodeProviders(dec); err != nil {
			return err
		}
	}
	if _, err := dec.Token(); err != nil {
		return err
	}
	if !seen {
		return fmt.Errorf("config: models file is missing the \"providers\" object")
	}
	// 尾随内容视为非法（对齐标准 json.Unmarshal 的行为），防止误改/误拼被静默接受
	if _, err := dec.Token(); err == io.EOF {
		return nil
	} else if err != nil {
		return err
	}
	return fmt.Errorf("config: unexpected trailing data after models file object")
}

// decodeProviders 解析 providers 对象，保持 key 的文件顺序
func (m *ModelsFile) decodeProviders(dec *json.Decoder) error {
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	if delim, ok := tok.(json.Delim); !ok || delim != '{' {
		return fmt.Errorf("config: \"providers\" must be a JSON object")
	}
	for dec.More() {
		keyTok, err := dec.Token()
		if err != nil {
			return err
		}
		id, ok := keyTok.(string)
		if !ok {
			return fmt.Errorf("config: providers keys must be strings")
		}
		var p Provider
		if err := dec.Decode(&p); err != nil {
			return err
		}
		// 同名 provider（大小写不敏感，注册表 key 会转小写）视为配置错误，
		// 与顶层重复 providers 键的处理保持一致
		for _, existing := range m.Providers {
			if strings.EqualFold(existing.ID, id) {
				return fmt.Errorf("config: providers has duplicate entry %q", id)
			}
		}
		m.Providers = append(m.Providers, NamedProvider{ID: id, Provider: p})
	}
	_, err = dec.Token()
	return err
}

// validate 校验 provider 条目；仅有 modelOverrides 的内置 provider 可以省略 baseUrl 与 models
func (m *ModelsFile) validate() error {
	for i := range m.Providers {
		provider := &m.Providers[i].Provider
		if provider.API == "" {
			provider.API = APIOpenAIResponses
			provider.apiDefaulted = true
		}
		if provider.API != APIOpenAIResponses && provider.API != APIOpenAICompletions {
			return fmt.Errorf("config: provider %q has unsupported api %q", m.Providers[i].ID, m.Providers[i].Provider.API)
		}
		if len(provider.Models) == 0 && len(provider.ModelOverrides) == 0 {
			return fmt.Errorf("config: provider %q has no models", m.Providers[i].ID)
		}
		for _, model := range provider.Models {
			if strings.TrimSpace(model.ID) == "" {
				return fmt.Errorf("config: provider %q has a model without id", m.Providers[i].ID)
			}
			if model.API != "" && model.API != APIOpenAIResponses && model.API != APIOpenAICompletions {
				return fmt.Errorf("config: provider %q model %q has unsupported api %q", m.Providers[i].ID, model.ID, model.API)
			}
			if model.Cost != nil && (model.Cost.Input < 0 || model.Cost.Output < 0 || model.Cost.CacheRead < 0 || model.Cost.CacheWrite < 0) {
				return fmt.Errorf("config: provider %q model %q has negative cost", m.Providers[i].ID, model.ID)
			}
		}
		for modelID := range provider.ModelOverrides {
			if strings.TrimSpace(modelID) == "" {
				return fmt.Errorf("config: provider %q has an override without model id", m.Providers[i].ID)
			}
		}
	}
	return nil
}

// envPattern 匹配 $VAR 与 ${VAR} 两种引用形式
var envPattern = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}|\$([A-Za-z_][A-Za-z0-9_]*)`)

// interpolate 把 apiKey 中的 $VAR / ${VAR} 替换为环境变量值；未设置的变量替换为空串，明文原样保留
func interpolate(s string) string {
	return envPattern.ReplaceAllStringFunc(s, func(match string) string {
		name := match
		if strings.HasPrefix(name, "${") {
			name = name[2 : len(name)-1]
		} else {
			name = name[1:]
		}
		return os.Getenv(name)
	})
}
