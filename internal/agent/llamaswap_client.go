package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"llm-swap/internal/protocol"
)

type RunningModelsClient interface {
	RunningModelsContext(context.Context) ([]protocol.RunningModel, error)
}

type HealthClient interface {
	HealthContext(context.Context) error
}

type LlamaSwapStateClient struct {
	BaseURL     string
	BearerToken string
	HTTP        *http.Client
}

func (c LlamaSwapStateClient) HealthContext(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(c.BaseURL, "/")+"/health", nil)
	if err != nil {
		return err
	}
	if c.BearerToken != "" {
		req.Header.Set("Authorization", "Bearer "+c.BearerToken)
	}

	httpClient := c.HTTP
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("llama-swap /health returned HTTP %d", resp.StatusCode)
	}
	return nil
}

func (c LlamaSwapStateClient) RunningModelsContext(ctx context.Context) ([]protocol.RunningModel, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(c.BaseURL, "/")+"/running", nil)
	if err != nil {
		return nil, err
	}
	if c.BearerToken != "" {
		req.Header.Set("Authorization", "Bearer "+c.BearerToken)
	}

	httpClient := c.HTTP
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("llama-swap /running returned HTTP %d", resp.StatusCode)
	}

	var raw any
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, err
	}
	return parseRunningModels(raw), nil
}

func parseRunningModels(raw any) []protocol.RunningModel {
	switch value := raw.(type) {
	case []any:
		return parseRunningModelArray(value)
	case map[string]any:
		for _, key := range []string{"running", "models"} {
			if nested, ok := value[key]; ok {
				return parseRunningModels(nested)
			}
		}
		if model, ok := stringField(value, "model"); ok {
			state, _ := stringField(value, "state")
			if state == "" {
				state = "ready"
			}
			return []protocol.RunningModel{{Model: model, State: state}}
		}
	}
	return nil
}

func parseRunningModelArray(values []any) []protocol.RunningModel {
	models := make([]protocol.RunningModel, 0, len(values))
	for _, value := range values {
		switch item := value.(type) {
		case string:
			if strings.TrimSpace(item) != "" {
				models = append(models, protocol.RunningModel{Model: strings.TrimSpace(item), State: "ready"})
			}
		case map[string]any:
			model, ok := stringField(item, "model")
			if !ok {
				model, ok = stringField(item, "name")
			}
			if !ok {
				continue
			}
			state, _ := stringField(item, "state")
			if state == "" {
				state = "ready"
			}
			models = append(models, protocol.RunningModel{Model: model, State: state})
		}
	}
	return models
}

func stringField(row map[string]any, key string) (string, bool) {
	value, ok := row[key]
	if !ok {
		return "", false
	}
	text, ok := value.(string)
	text = strings.TrimSpace(text)
	return text, ok && text != ""
}
