package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

type LlamaSwapClient struct {
	BearerToken string
	HTTPClient  *http.Client
}

type HTTPStatusError struct {
	StatusCode int
	Status     string
	Body       []byte
}

func (e HTTPStatusError) Error() string {
	if len(e.Body) == 0 {
		return fmt.Sprintf("llama-swap returned %s", e.Status)
	}
	return fmt.Sprintf("llama-swap returned %s: %s", e.Status, string(e.Body))
}

func (c LlamaSwapClient) Unload(ctx context.Context, baseURL, model string) error {
	endpoint := strings.TrimRight(baseURL, "/") + "/api/models/unload/" + url.PathEscape(model)
	return c.post(ctx, endpoint)
}

func (c LlamaSwapClient) Load(ctx context.Context, baseURL, model string) error {
	endpoint := strings.TrimRight(baseURL, "/") + "/upstream/" + url.PathEscape(model) + "/v1/models"
	return c.get(ctx, endpoint)
}

func (c LlamaSwapClient) UnloadAll(ctx context.Context, baseURL string) error {
	endpoint := strings.TrimRight(baseURL, "/") + "/api/models/unload"
	return c.post(ctx, endpoint)
}

func (c LlamaSwapClient) post(ctx context.Context, endpoint string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, nil)
	if err != nil {
		return err
	}
	return c.do(req)
}

func (c LlamaSwapClient) get(ctx context.Context, endpoint string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	return c.do(req)
}

func (c LlamaSwapClient) do(req *http.Request) error {
	if c.BearerToken != "" {
		req.Header.Set("Authorization", "Bearer "+c.BearerToken)
	}

	httpClient := c.HTTPClient
	if httpClient == nil {
		httpClient = http.DefaultClient
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return HTTPStatusError{StatusCode: resp.StatusCode, Status: resp.Status, Body: body}
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	return nil
}

func ExtractModel(body []byte) string {
	var req struct {
		Model string `json:"model"`
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	if err := decoder.Decode(&req); err != nil {
		return ""
	}
	return strings.TrimSpace(req.Model)
}
