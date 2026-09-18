package llm

import (
	"context"
	"fmt"
)

type Provider interface {
	ID() string
	Models() []Model
	Stream(ctx context.Context, model Model, c Context, opts Options) (<-chan Event, error)
}

type Models struct {
	providers map[string]Provider
}

func NewModels() *Models {
	return &Models{providers: map[string]Provider{}}
}

func (m *Models) Register(p Provider) {
	m.providers[p.ID()] = p
}

func (m *Models) GetModel(providerID, modelID string) (Model, error) {
	p, err := m.provider(providerID)
	if err != nil {
		return Model{}, err
	}
	for _, model := range p.Models() {
		if model.ID == modelID {
			return model, nil
		}
	}
	return Model{}, fmt.Errorf("llm: 厂商 %q 下没有模型 %q", providerID, modelID)
}

func (m *Models) StreamFn(providerID, modelID string) (StreamFn, error) {
	p, err := m.provider(providerID)
	if err != nil {
		return nil, err
	}
	for _, model := range p.Models() {
		if model.ID == modelID {
			return p.Stream, nil
		}
	}
	return nil, fmt.Errorf("llm: 厂商 %q 下没有模型 %q", providerID, modelID)
}

func (m *Models) provider(providerID string) (Provider, error) {
	p, ok := m.providers[providerID]
	if !ok {
		return nil, fmt.Errorf("llm: 未注册的厂商 %q", providerID)
	}
	return p, nil
}
