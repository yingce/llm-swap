package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"llm-swap/internal/protocol"
)

func TestConfigClientGetConfigForAgentSendsIdentityAndKeepsLegacyHelper(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.Method != http.MethodGet || r.URL.Path != "/internal/agent/config" {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer agent-token" {
			t.Fatalf("authorization=%q", got)
		}
		if got := r.URL.Query().Get("tags"); got != "gpu 4090,gpu/foo" {
			t.Fatalf("tags=%q", got)
		}
		if requests == 1 {
			if got := r.URL.Query().Get("agent_id"); got != "worker/gpu 0" {
				t.Fatalf("agent_id=%q", got)
			}
		} else if _, present := r.URL.Query()["agent_id"]; present {
			t.Fatalf("legacy helper sent agent_id in %q", r.URL.RawQuery)
		}
		_ = json.NewEncoder(w).Encode(protocol.AgentConfigResponse{})
	}))
	defer server.Close()
	client := ConfigClient{BaseURL: server.URL + "/", Token: "agent-token", HTTP: server.Client()}

	if _, err := client.GetConfigForAgentContext(context.Background(), "worker/gpu 0", []string{"gpu 4090", "gpu/foo"}); err != nil {
		t.Fatalf("identity-aware config: %v", err)
	}
	if _, err := client.GetConfig([]string{"gpu 4090", "gpu/foo"}); err != nil {
		t.Fatalf("legacy config: %v", err)
	}
}

func TestConfigClientRequestsAndReleasesTransportLease(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.Method != http.MethodPost || r.URL.Path != "/internal/agent/transport/lease" {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer agent-token" {
			t.Fatalf("authorization=%q", got)
		}
		if got := r.Header.Get("Content-Type"); got != "application/json" {
			t.Fatalf("content-type=%q", got)
		}
		var request protocol.TransportLeaseRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		if requests == 1 {
			if request.AgentID != "worker-gpu0" || request.Generation != 7 || request.LeaseID != "current" || request.Release || len(request.ExcludeSlots) != 1 || request.ExcludeSlots[0] != 2 || len(request.Tags) != 1 || request.Tags[0] != "gpu-4090" {
				t.Fatalf("acquire request=%+v", request)
			}
			_ = json.NewEncoder(w).Encode(protocol.TransportLeaseResponse{LeaseID: "next", Slot: 3, RemotePort: 2003, Generation: 7})
			return
		}
		if !request.Release || request.LeaseID != "next" || len(request.Tags) != 1 || request.Tags[0] != "gpu-4090" {
			t.Fatalf("release request=%+v", request)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	client := ConfigClient{BaseURL: server.URL, Token: "agent-token", HTTP: server.Client()}

	lease, err := client.RequestTransportLeaseContext(context.Background(), protocol.TransportLeaseRequest{
		AgentID: "worker-gpu0", Tags: []string{"gpu-4090"}, Generation: 7, LeaseID: "current", ExcludeSlots: []int{2},
	})
	if err != nil {
		t.Fatalf("request lease: %v", err)
	}
	if lease.LeaseID != "next" || lease.Slot != 3 || lease.RemotePort != 2003 || lease.Generation != 7 {
		t.Fatalf("lease=%+v", lease)
	}
	if err := client.ReleaseTransportLeaseContext(context.Background(), protocol.TransportLeaseRequest{
		AgentID: "worker-gpu0", Tags: []string{"gpu-4090"}, Generation: 7, LeaseID: "next",
	}); err != nil {
		t.Fatalf("release lease: %v", err)
	}
}

func TestConfigClientTransportStatusErrorsAreGeneric(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "server-secret-detail", http.StatusConflict)
	}))
	defer server.Close()
	client := ConfigClient{BaseURL: server.URL, Token: "agent-token", HTTP: server.Client()}

	_, err := client.GetConfigForAgentContext(context.Background(), "worker-gpu0", []string{"gpu-4090"})
	if err == nil || strings.Contains(err.Error(), "server-secret-detail") {
		t.Fatalf("config error=%v", err)
	}
	_, err = client.RequestTransportLeaseContext(context.Background(), protocol.TransportLeaseRequest{AgentID: "worker-gpu0", Generation: 1})
	if err == nil || strings.Contains(err.Error(), "server-secret-detail") {
		t.Fatalf("lease error=%v", err)
	}
	err = client.ReleaseTransportLeaseContext(context.Background(), protocol.TransportLeaseRequest{AgentID: "worker-gpu0", Generation: 1, LeaseID: "lease"})
	if err == nil || strings.Contains(err.Error(), "server-secret-detail") {
		t.Fatalf("release error=%v", err)
	}
}

func TestReconcilerUsesIdentityAwareConfigClientWhenAvailable(t *testing.T) {
	client := &identityAwareGatewayProbe{}
	reconciler := Reconciler{AgentID: "worker-gpu0", Tags: []string{"gpu-4090"}, Gateway: client}

	if _, err := reconciler.getConfigContext(context.Background()); err != nil {
		t.Fatal(err)
	}
	if client.agentID != "worker-gpu0" || len(client.tags) != 1 || client.tags[0] != "gpu-4090" {
		t.Fatalf("identity-aware request id=%q tags=%v", client.agentID, client.tags)
	}
	if client.legacyCalls != 0 {
		t.Fatalf("legacy config calls=%d, want 0", client.legacyCalls)
	}
}

type identityAwareGatewayProbe struct {
	agentID     string
	tags        []string
	legacyCalls int
}

func (p *identityAwareGatewayProbe) GetConfigForAgentContext(_ context.Context, agentID string, tags []string) (protocol.AgentConfigResponse, error) {
	p.agentID = agentID
	p.tags = append([]string(nil), tags...)
	return protocol.AgentConfigResponse{}, nil
}

func (p *identityAwareGatewayProbe) GetConfigContext(context.Context, []string) (protocol.AgentConfigResponse, error) {
	p.legacyCalls++
	return protocol.AgentConfigResponse{}, nil
}

func (p *identityAwareGatewayProbe) HeartbeatContext(context.Context, protocol.HeartbeatRequest) (protocol.HeartbeatResponse, error) {
	return protocol.HeartbeatResponse{}, nil
}
