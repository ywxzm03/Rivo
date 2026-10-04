package app

import (
	"fmt"
	"os"
	"strings"

	"github.com/joho/godotenv"
)

type Config struct {
	ProviderID string
	ModelID    string
	APIKey     string
	BaseURL    string
}

func ConfigFromEnv() (Config, error) {
	if err := godotenv.Load(".env"); err != nil {
		return Config{}, fmt.Errorf("failed to load .env: %w", err)
	}

	spec, err := lookupProvider(os.Getenv(providerEnv))
	if err != nil {
		return Config{}, err
	}

	apiKey := strings.TrimSpace(os.Getenv(apiKeyEnv))
	if apiKey == "" {
		return Config{}, fmt.Errorf("%s is not set", apiKeyEnv)
	}

	modelID := strings.TrimSpace(os.Getenv(modelEnv))
	if modelID == "" {
		modelID = spec.defaultModel
	}

	return Config{
		ProviderID: spec.id,
		ModelID:    modelID,
		APIKey:     apiKey,
		BaseURL:    strings.TrimSpace(os.Getenv(baseURLEnv)),
	}, nil
}
