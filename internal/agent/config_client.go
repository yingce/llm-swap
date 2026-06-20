package agent

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"llm-swap/internal/protocol"
)

type ConfigClient struct {
	BaseURL string
	Token   string
	HTTP    *http.Client
}

func (c ConfigClient) GetConfig(tags []string) (protocol.AgentConfigResponse, error) {
	u := strings.TrimRight(c.BaseURL, "/") + "/internal/agent/config?tags=" + url.QueryEscape(strings.Join(tags, ","))
	req, err := http.NewRequest(http.MethodGet, u, nil)
	if err != nil {
		return protocol.AgentConfigResponse{}, err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)

	resp, err := c.httpClient().Do(req)
	if err != nil {
		return protocol.AgentConfigResponse{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return protocol.AgentConfigResponse{}, fmt.Errorf("get agent config returned HTTP %d", resp.StatusCode)
	}

	var out protocol.AgentConfigResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return protocol.AgentConfigResponse{}, err
	}
	return out, nil
}

func (c ConfigClient) Heartbeat(hb protocol.HeartbeatRequest) (protocol.HeartbeatResponse, error) {
	data, err := json.Marshal(hb)
	if err != nil {
		return protocol.HeartbeatResponse{}, err
	}
	req, err := http.NewRequest(http.MethodPost, strings.TrimRight(c.BaseURL, "/")+"/internal/agent/heartbeat", bytes.NewReader(data))
	if err != nil {
		return protocol.HeartbeatResponse{}, err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient().Do(req)
	if err != nil {
		return protocol.HeartbeatResponse{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return protocol.HeartbeatResponse{}, fmt.Errorf("agent heartbeat returned HTTP %d", resp.StatusCode)
	}

	var out protocol.HeartbeatResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return protocol.HeartbeatResponse{}, err
	}
	return out, nil
}

func (c ConfigClient) httpClient() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return http.DefaultClient
}
