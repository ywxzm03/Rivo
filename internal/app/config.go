package app

import (
	"fmt"
	"os"
	"strings"

	"github.com/joho/godotenv"
	"github.com/ywxzm03/Rivo/internal/llm/provider/openai"
)

const (
	apiKeyEnv  = "OPENAI_API_KEY"
	baseURLEnv = "OPENAI_BASE_URL"
	modelEnv   = "OPENAI_MODEL"
)

const defaultModel = "gpt-5.6-sol"

type Config struct {
	ProviderID string
	ModelID    string
	APIKey     string
	BaseURL    string
}

func ConfigFromEnv() (Config, error) {
	err := godotenv.Load(".env")
	if err != nil {
		return Config{}, fmt.Errorf("Error loading .env file: %w", err)
	}

	apiKey := strings.TrimSpace(os.Getenv(apiKeyEnv))
	if apiKey == "" {
		return Config{}, fmt.Errorf("%s 未设置", apiKeyEnv)
	}

	modelID := strings.TrimSpace(os.Getenv(modelEnv))
	if modelID == "" {
		modelID = defaultModel
	}

	return Config{
		ProviderID: openai.ProviderID,
		ModelID:    modelID,
		APIKey:     apiKey,
		BaseURL:    strings.TrimSpace(os.Getenv(baseURLEnv)),
	}, nil
}
