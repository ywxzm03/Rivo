package provider

import (
	"testing"

	"github.com/ywxzm03/Rivo/internal/config"
	"github.com/ywxzm03/Rivo/internal/llm"
)

func TestCustomSpecCopiesModelCost(t *testing.T) {
	spec, err := customSpec("custom", config.Provider{
		BaseURL: "http://custom/v1",
		Models: []config.Model{{
			ID:   "model",
			Cost: &llm.CostRates{Input: 0.15, Output: 0.6, CacheRead: 0.003},
		}},
	})
	if err != nil {
		t.Fatalf("customSpec() error = %v", err)
	}
	if len(spec.models) != 1 || spec.models[0].Cost == nil {
		t.Fatalf("model cost = %+v, want configured rates", spec.models)
	}
	if got := *spec.models[0].Cost; got != (llm.CostRates{Input: 0.15, Output: 0.6, CacheRead: 0.003}) {
		t.Fatalf("model cost = %+v, want configured rates", got)
	}
}
