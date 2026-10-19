package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ywxzm03/Rivo/internal/config"
	"github.com/ywxzm03/Rivo/internal/llm"
	"github.com/ywxzm03/Rivo/internal/llm/provider/deepseek"
	"github.com/ywxzm03/Rivo/internal/llm/provider/openai"
)

// useConfigHome 把配置目录切到临时目录（RIVO_AGENT_DIR 覆盖，跨平台一致）
func useConfigHome(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("RIVO_AGENT_DIR", dir)
	// 清除本机可能导出的内置 provider 凭据，保证解析链测试不受开发环境干扰；
	// 需要凭据的测试在调用本 helper 之后自行设置
	t.Setenv("DEEPSEEK_API_KEY", "")
	t.Setenv("OPENAI_API_KEY", "")
	return dir
}

func writeAgentConfig(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestLoadWithoutAnyConfigErrors(t *testing.T) {
	useConfigHome(t)
	_, err := Load()
	if err == nil || !strings.Contains(err.Error(), "no models available") {
		t.Fatalf("Load() error = %v, want no-models error", err)
	}
}

func TestLoadUsesBuiltinProviderEnvKey(t *testing.T) {
	useConfigHome(t)
	t.Setenv("DEEPSEEK_API_KEY", "env-key")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.ProviderID != deepseek.ProviderID || cfg.ModelID != deepseek.DefaultModel {
		t.Errorf("Load() = %s/%s, want builtin deepseek default", cfg.ProviderID, cfg.ModelID)
	}
}

func TestLoadPrefersSettingsPairedDefault(t *testing.T) {
	dir := useConfigHome(t)
	t.Setenv("DEEPSEEK_API_KEY", "env-key")
	writeAgentConfig(t, dir, "settings.json", `{"defaultProvider":"magpie","defaultModel":"glm-5.3"}`)
	writeAgentConfig(t, dir, "models.json", `{"providers":{"magpie":{"baseUrl":"http://127.0.0.1:3425/v1","apiKey":"magpie","models":[{"id":"glm-5.3"}]}}}`)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.ProviderID != "magpie" || cfg.ModelID != "glm-5.3" {
		t.Errorf("Load() = %s/%s, want settings default", cfg.ProviderID, cfg.ModelID)
	}
}

func TestLoadFallsBackWhenSettingsModelUnknown(t *testing.T) {
	dir := useConfigHome(t)
	t.Setenv("DEEPSEEK_API_KEY", "env-key")
	writeAgentConfig(t, dir, "settings.json", `{"defaultProvider":"magpie","defaultModel":"gone"}`)
	writeAgentConfig(t, dir, "models.json", `{"providers":{"magpie":{"baseUrl":"http://x/v1","apiKey":"k","models":[{"id":"other"}]}}}`)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	// settings 指定的模型不存在时软回退到内置默认（对齐 pi）
	if cfg.ProviderID != deepseek.ProviderID {
		t.Errorf("Load() provider = %q, want soft fallback to deepseek", cfg.ProviderID)
	}
}

func TestLoadFallsBackWhenSettingsProviderHasNoKey(t *testing.T) {
	dir := useConfigHome(t)
	t.Setenv("DEEPSEEK_API_KEY", "env-key")
	writeAgentConfig(t, dir, "settings.json", `{"defaultProvider":"magpie","defaultModel":"glm-5.3"}`)
	writeAgentConfig(t, dir, "models.json", `{"providers":{"magpie":{"baseUrl":"http://x/v1","models":[{"id":"glm-5.3"}]}}}`)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	// provider 无凭据视同不可用（对齐 pi hasConfiguredAuth）
	if cfg.ProviderID != deepseek.ProviderID {
		t.Errorf("Load() provider = %q, want soft fallback to deepseek", cfg.ProviderID)
	}
}

func TestLoadSingleSideSettingsIsIgnored(t *testing.T) {
	dir := useConfigHome(t)
	t.Setenv("DEEPSEEK_API_KEY", "env-key")
	// 只设 defaultProvider 不生效（对齐 pi：必须成对）
	writeAgentConfig(t, dir, "settings.json", `{"defaultProvider":"magpie"}`)
	writeAgentConfig(t, dir, "models.json", `{"providers":{"magpie":{"baseUrl":"http://x/v1","apiKey":"k","models":[{"id":"glm-5.3"}]}}}`)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.ProviderID != deepseek.ProviderID {
		t.Errorf("Load() provider = %q, want builtin default (single-side settings ignored)", cfg.ProviderID)
	}
}

func TestLoadSoftDegradesOnBrokenModelsFile(t *testing.T) {
	dir := useConfigHome(t)
	t.Setenv("DEEPSEEK_API_KEY", "env-key")
	writeAgentConfig(t, dir, "models.json", "{oops")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	// 解析失败软降级：警告 + 按空配置继续走内置链
	if len(cfg.Warnings) == 0 || !strings.Contains(cfg.Warnings[0], "invalid models file") {
		t.Errorf("Warnings = %v, want models-file parse warning", cfg.Warnings)
	}
	if cfg.ProviderID != deepseek.ProviderID {
		t.Errorf("Load() provider = %q, want builtin fallback", cfg.ProviderID)
	}
}

func TestLoadFirstAvailableProviderInFileOrder(t *testing.T) {
	dir := useConfigHome(t)
	writeAgentConfig(t, dir, "models.json", `{"providers":{
		"no-key": {"baseUrl":"http://a/v1","models":[{"id":"m1"}]},
		"with-key": {"baseUrl":"http://b/v1","apiKey":"k","models":[{"id":"m2","contextWindow":1000,"maxTokens":500,"reasoning":true}]}
	}}`)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.ProviderID != "with-key" || cfg.ModelID != "m2" {
		t.Errorf("Load() = %s/%s, want first provider with credentials", cfg.ProviderID, cfg.ModelID)
	}
}

func TestLoadReadsDefaultThinkingLevel(t *testing.T) {
	dir := useConfigHome(t)
	t.Setenv("DEEPSEEK_API_KEY", "env-key")
	writeAgentConfig(t, dir, "settings.json", `{"defaultThinkingLevel":"high"}`)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.ThinkingLevel != llm.ThinkingLevelHigh {
		t.Errorf("ThinkingLevel = %q, want high", cfg.ThinkingLevel)
	}
}

func TestLoadWarnsOnUnknownThinkingLevel(t *testing.T) {
	dir := useConfigHome(t)
	t.Setenv("DEEPSEEK_API_KEY", "env-key")
	writeAgentConfig(t, dir, "settings.json", `{"defaultThinkingLevel":"ultra"}`)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.ThinkingLevel != "" {
		t.Errorf("ThinkingLevel = %q, want empty for unknown level", cfg.ThinkingLevel)
	}
	if len(cfg.Warnings) == 0 || !strings.Contains(cfg.Warnings[0], "defaultThinkingLevel") {
		t.Errorf("Warnings = %v, want unknown-level warning", cfg.Warnings)
	}
}

func TestLoadCustomProviderAssemblesOpenAICompatibleSpec(t *testing.T) {
	dir := useConfigHome(t)
	writeAgentConfig(t, dir, "models.json", `{"providers":{"magpie":{
		"baseUrl":"http://127.0.0.1:3425/v1","apiKey":"magpie",
		"models":[{"id":"glm-5.3","name":"GLM","contextWindow":200000,"maxTokens":8192,"reasoning":true,"supportsImages":true,
			"thinkingLevelMap":{"off":null,"medium":"medium"},"samplingParams":{"temperature":0.7}}]
	}}}`)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.ProviderID != "magpie" {
		t.Fatalf("Load() provider = %q, want magpie", cfg.ProviderID)
	}
	// 注册表里的自定义 spec 能装配出完整模型元数据
	spec, err := cfg.lookupProvider("magpie")
	if err != nil {
		t.Fatalf("lookupProvider() error = %v", err)
	}
	if len(spec.models) != 1 {
		t.Fatalf("models = %d, want 1", len(spec.models))
	}
	m := spec.models[0]
	if m.Name != "GLM" || m.ContextWindow != 200000 || m.MaxTokens != 8192 || !m.Reasoning || !m.SupportsImages {
		t.Errorf("model = %+v, want mapped metadata", m)
	}
	if m.ThinkingLevelMap[llm.ThinkingLevelOff] != nil {
		t.Errorf("thinkingLevelMap[off] = %v, want disabled (nil)", m.ThinkingLevelMap[llm.ThinkingLevelOff])
	}
	if m.SamplingParams["temperature"] != 0.7 {
		t.Errorf("samplingParams = %v, want temperature 0.7", m.SamplingParams)
	}
	// 装配出的流函数非空（openai 兼容客户端离线构造）
	models, streamFn := spec.new(spec.apiKey, spec.baseURL)
	if len(models) != 1 || streamFn == nil {
		t.Errorf("spec.new() = %d models, streamFn %v, want one model and a stream fn", len(models), streamFn)
	}
}

func TestLoadCustomProviderModelOverride(t *testing.T) {
	dir := useConfigHome(t)
	writeAgentConfig(t, dir, "models.json", `{"providers":{"magpie":{
		"baseUrl":"http://127.0.0.1:3425/v1","apiKey":"magpie",
		"models":[{"id":"glm-5.3","contextWindow":200000}],
		"modelOverrides":{"glm-5.3":{"contextWindow":64000,"maxTokens":4096}}
	}}}`)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	spec, err := cfg.lookupProvider("magpie")
	if err != nil {
		t.Fatal(err)
	}
	model, err := findModel("magpie", spec.models, "glm-5.3")
	if err != nil {
		t.Fatal(err)
	}
	if model.ContextWindow != 64000 || model.MaxTokens != 4096 {
		t.Errorf("model limits = %d/%d, want 64000/4096", model.ContextWindow, model.MaxTokens)
	}
}

func TestLoadBuiltinModelDefinitionUsesPIContextDefault(t *testing.T) {
	dir := useConfigHome(t)
	t.Setenv("OPENAI_API_KEY", "env-key")
	writeAgentConfig(t, dir, "models.json", `{"providers":{"openai":{"models":[{"id":"`+openai.DefaultModel+`"}]}}}`)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	spec, err := cfg.lookupProvider(openai.ProviderID)
	if err != nil {
		t.Fatal(err)
	}
	model, err := findModel(openai.ProviderID, spec.models, openai.DefaultModel)
	if err != nil {
		t.Fatal(err)
	}
	if model.ContextWindow != defaultContextWindow || model.MaxTokens != defaultMaxTokens {
		t.Errorf("model defaults = %d/%d, want %d/%d", model.ContextWindow, model.MaxTokens, defaultContextWindow, defaultMaxTokens)
	}
}

func TestLoadCustomProviderUsesPIImageInputCapability(t *testing.T) {
	dir := useConfigHome(t)
	writeAgentConfig(t, dir, "models.json", `{"providers":{"magpie":{
		"baseUrl":"http://127.0.0.1:3425/v1","apiKey":"magpie",
		"models":[{"id":"workbuddy-ai/deepseek-v4.1-flash","input":["text","image"]}]
	}}}`)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	spec, err := cfg.lookupProvider("magpie")
	if err != nil {
		t.Fatalf("lookupProvider() error = %v", err)
	}
	if len(spec.models) != 1 || !spec.models[0].SupportsImages {
		t.Fatalf("model capability = %+v, want SupportsImages=true from input=image", spec.models)
	}
}

func TestLoadCustomProviderSelectsChatCompletionsAPI(t *testing.T) {
	dir := useConfigHome(t)
	writeAgentConfig(t, dir, "models.json", `{"providers":{"openai":{"baseUrl":"http://127.0.0.1:3425/v1","api":"openai-completions","apiKey":"openai","models":[{"id":"gpt-5.6-sol","api":"openai-completions"}]}}}`)
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	spec, err := cfg.lookupProvider("openai")
	if err != nil {
		t.Fatal(err)
	}
	model, err := findModel(spec.id, spec.models, "gpt-5.6-sol")
	if err != nil || model.API != llm.APIOpenAICompletions {
		t.Fatalf("model = %#v, error = %v, want chat completions model", model, err)
	}
	if len(spec.models) != 4 {
		t.Fatalf("models = %d, want builtins plus configured model", len(spec.models))
	}
}

func TestLoadBuiltinModelOverridePreservesProviderModels(t *testing.T) {
	dir := useConfigHome(t)
	t.Setenv("OPENAI_API_KEY", "env-key")
	writeAgentConfig(t, dir, "models.json", `{"providers":{"openai":{"modelOverrides":{"`+openai.DefaultModel+`":{"contextWindow":64000}}}}}`)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	spec, err := cfg.lookupProvider(openai.ProviderID)
	if err != nil {
		t.Fatal(err)
	}
	model, err := findModel(openai.ProviderID, spec.models, openai.DefaultModel)
	if err != nil {
		t.Fatal(err)
	}
	if model.ContextWindow != 64000 {
		t.Errorf("ContextWindow = %d, want 64000", model.ContextWindow)
	}
	if len(spec.models) != 3 {
		t.Errorf("models = %d, want all built-in models preserved", len(spec.models))
	}
	a, err := NewWithSessionDir(cfg, t.TempDir(), nil)
	if err != nil {
		t.Fatalf("NewWithSessionDir() error = %v", err)
	}
	if a.CurrentModel().ContextWindow != 64000 || a.coordinator.Compactor == nil || a.coordinator.Compactor.Config.ContextWindow != 64000 {
		t.Errorf("runtime context window = model %d, compactor %+v; want 64000", a.CurrentModel().ContextWindow, a.coordinator.Compactor)
	}
}

func TestLoadModelsOverrideWinsOverModelDefinition(t *testing.T) {
	dir := useConfigHome(t)
	t.Setenv("OPENAI_API_KEY", "env-key")
	writeAgentConfig(t, dir, "models.json", `{"providers":{"openai":{
		"models":[{"id":"`+openai.DefaultModel+`","contextWindow":90000}],
		"modelOverrides":{"`+openai.DefaultModel+`":{"contextWindow":64000}}
	}}}`)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	spec, err := cfg.lookupProvider(openai.ProviderID)
	if err != nil {
		t.Fatal(err)
	}
	model, err := findModel(openai.ProviderID, spec.models, openai.DefaultModel)
	if err != nil {
		t.Fatal(err)
	}
	if model.ContextWindow != 64000 {
		t.Errorf("ContextWindow = %d, want override value 64000", model.ContextWindow)
	}
}

func TestLoadInvalidBuiltinOverrideFallsBackWithWarning(t *testing.T) {
	dir := useConfigHome(t)
	t.Setenv("OPENAI_API_KEY", "env-key")
	writeAgentConfig(t, dir, "models.json", `{"providers":{"openai":{"modelOverrides":{"`+openai.DefaultModel+`":{"contextWindow":0}}}}}`)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if len(cfg.Warnings) == 0 || !strings.Contains(cfg.Warnings[0], "overlay ignored") {
		t.Fatalf("Warnings = %v, want overlay warning", cfg.Warnings)
	}
	spec, err := cfg.lookupProvider(openai.ProviderID)
	if err != nil {
		t.Fatal(err)
	}
	model, err := findModel(openai.ProviderID, spec.models, openai.DefaultModel)
	if err != nil {
		t.Fatal(err)
	}
	if model.ContextWindow != 1_050_000 {
		t.Errorf("ContextWindow = %d, want original builtin value", model.ContextWindow)
	}
}

func TestAvailableModelsUsesRegistryContents(t *testing.T) {
	wanted := llm.Model{ID: "configured", Provider: "custom"}
	cfg := Config{registry: map[string]providerSpec{
		"custom": {models: []llm.Model{wanted}},
	}}

	models := cfg.AvailableModels()
	if len(models) != 1 || models[0].ID != wanted.ID || models[0].Provider != wanted.Provider {
		t.Fatalf("AvailableModels() = %+v, want only configured model", models)
	}
}

func TestLoadSettingsPairedDefaultWithBuiltinProvider(t *testing.T) {
	dir := useConfigHome(t)
	t.Setenv("OPENAI_API_KEY", "env-key")
	writeAgentConfig(t, dir, "settings.json", `{"defaultProvider":"openai","defaultModel":"`+openai.DefaultModel+`"}`)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	// 内置 provider 的模型列表也要参与 settings 成对校验（回归：spec.models 曾恒为 nil）
	if cfg.ProviderID != openai.ProviderID || cfg.ModelID != openai.DefaultModel {
		t.Errorf("Load() = %s/%s, want settings default with builtin provider", cfg.ProviderID, cfg.ModelID)
	}
}

func TestNewSessionAppliesConfiguredThinkingLevel(t *testing.T) {
	cfg := configWithBuiltin(deepseek.ProviderID, deepseek.DefaultModel)
	cfg.ThinkingLevel = llm.ThinkingLevelHigh

	a, err := New(cfg, nil)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	// 新会话也要应用 settings 解析出的 defaultThinkingLevel
	if a.agent.AgentConfig.ThinkingLevel != llm.ThinkingLevelHigh {
		t.Errorf("agent ThinkingLevel = %q, want high", a.agent.AgentConfig.ThinkingLevel)
	}
}

func TestCustomSpecRejectsEmptyModels(t *testing.T) {
	if _, err := customSpec("x", config.Provider{BaseURL: "http://x/v1"}); err == nil {
		t.Fatal("customSpec() error = nil, want empty-models error")
	}
}

func TestLoadIncludesParseWarningsInNoModelsError(t *testing.T) {
	dir := useConfigHome(t)
	writeAgentConfig(t, dir, "models.json", "{oops")

	_, err := Load()
	if err == nil || !strings.Contains(err.Error(), "no models available") {
		t.Fatalf("Load() error = %v, want no-models error", err)
	}
	// 只配置了自定义 provider 时，解析失败的原因必须并入错误信息，不能只报"未配置"
	if !strings.Contains(err.Error(), "invalid models file") {
		t.Errorf("Load() error = %v, want it to include the parse failure", err)
	}
}

func TestLoadRejectsDuplicateProviderID(t *testing.T) {
	dir := useConfigHome(t)
	writeAgentConfig(t, dir, "models.json", `{"providers":{
		"magpie": {"baseUrl":"http://a/v1","apiKey":"k","models":[{"id":"m1"}]},
		"magpie": {"baseUrl":"http://b/v1","apiKey":"k","models":[{"id":"m2"}]}
	}}`)

	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "duplicate entry") {
		t.Fatalf("Load() error = %v, want duplicate provider entry error", err)
	}
}

func TestLoadNoModelsErrorIncludesParseWarning(t *testing.T) {
	dir := useConfigHome(t)
	// 只配置了自定义 provider 且文件解析失败：错误必须包含真实原因，而非仅 "no models available"
	writeAgentConfig(t, dir, "models.json", "{oops")

	_, err := Load()
	if err == nil || !strings.Contains(err.Error(), "no models available") {
		t.Fatalf("Load() error = %v, want no-models error", err)
	}
	if !strings.Contains(err.Error(), "invalid models file") {
		t.Errorf("Load() error = %v, want it to include the parse failure", err)
	}
}
