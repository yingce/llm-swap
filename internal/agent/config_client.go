package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"llm-swap/internal/protocol"
)

// HeartbeatTransportLeaseConflictError is returned only when the gateway
// rejects the lease identity carried by a heartbeat. It intentionally carries
// no response body or request values because both may contain credentials.
type HeartbeatTransportLeaseConflictError struct{}

func (*HeartbeatTransportLeaseConflictError) Error() string {
	return "agent heartbeat transport lease conflict"
}

type ConfigClient struct {
	BaseURL string
	Token   string
	HTTP    *http.Client
}

func (c ConfigClient) GetConfig(tags []string) (protocol.AgentConfigResponse, error) {
	return c.GetConfigContext(context.Background(), tags)
}

func (c ConfigClient) GetConfigContext(ctx context.Context, tags []string) (protocol.AgentConfigResponse, error) {
	return c.GetConfigForAgentContext(ctx, "", tags)
}

func (c ConfigClient) GetConfigForAgent(agentID string, tags []string) (protocol.AgentConfigResponse, error) {
	return c.GetConfigForAgentContext(context.Background(), agentID, tags)
}

func (c ConfigClient) GetConfigForAgentContext(ctx context.Context, agentID string, tags []string) (protocol.AgentConfigResponse, error) {
	query := url.Values{}
	query.Set("tags", strings.Join(tags, ","))
	if strings.TrimSpace(agentID) != "" {
		query.Set("agent_id", agentID)
	}
	u := strings.TrimRight(c.BaseURL, "/") + "/internal/agent/config?" + query.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
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

func (c ConfigClient) RequestTransportLease(request protocol.TransportLeaseRequest) (protocol.TransportLeaseResponse, error) {
	return c.RequestTransportLeaseContext(context.Background(), request)
}

func (c ConfigClient) RequestTransportLeaseContext(ctx context.Context, request protocol.TransportLeaseRequest) (protocol.TransportLeaseResponse, error) {
	data, err := json.Marshal(request)
	if err != nil {
		return protocol.TransportLeaseResponse{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(c.BaseURL, "/")+"/internal/agent/transport/lease", bytes.NewReader(data))
	if err != nil {
		return protocol.TransportLeaseResponse{}, err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient().Do(req)
	if err != nil {
		return protocol.TransportLeaseResponse{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return protocol.TransportLeaseResponse{}, fmt.Errorf("transport lease request returned HTTP %d", resp.StatusCode)
	}
	if resp.StatusCode == http.StatusNoContent {
		return protocol.TransportLeaseResponse{}, nil
	}
	var out protocol.TransportLeaseResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return protocol.TransportLeaseResponse{}, err
	}
	return out, nil
}

func (c ConfigClient) ReleaseTransportLease(request protocol.TransportLeaseRequest) error {
	return c.ReleaseTransportLeaseContext(context.Background(), request)
}

func (c ConfigClient) ReleaseTransportLeaseContext(ctx context.Context, request protocol.TransportLeaseRequest) error {
	request.Release = true
	_, err := c.RequestTransportLeaseContext(ctx, request)
	return err
}

func (c ConfigClient) Heartbeat(hb protocol.HeartbeatRequest) (protocol.HeartbeatResponse, error) {
	return c.HeartbeatContext(context.Background(), hb)
}

func (c ConfigClient) HeartbeatContext(ctx context.Context, hb protocol.HeartbeatRequest) (protocol.HeartbeatResponse, error) {
	data, err := json.Marshal(hb)
	if err != nil {
		return protocol.HeartbeatResponse{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(c.BaseURL, "/")+"/internal/agent/heartbeat", bytes.NewReader(data))
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
	if resp.StatusCode == http.StatusConflict {
		return protocol.HeartbeatResponse{}, &HeartbeatTransportLeaseConflictError{}
	}
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
