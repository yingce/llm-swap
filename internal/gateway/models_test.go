package gateway

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"llm-swap/internal/protocol"
)

func TestModelsEndpointListsOnlySchedulableModels(t *testing.T) {
	srv := NewServer(testProxyConfig())
	srv.workers.UpsertHeartbeat(protocol.HeartbeatRequest{
		AgentID:      "worker-a",
		Tags:         []string{"gpu-4090"},
		LlamaSwapURL: "http://worker",
		Artifacts:    map[string]string{"qwen": "ready", "other": "ready"},
		RunningModels: []protocol.RunningModel{
			{Model: "qwen", State: "ready"},
		},
	}, time.Now())

	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	req.Header.Set("Authorization", "Bearer client-secret")
	rr := httptest.NewRecorder()

	srv.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d: %s", rr.Code, http.StatusOK, rr.Body.String())
	}

	var resp struct {
		Object string `json:"object"`
		Data   []struct {
			ID      string `json:"id"`
			Object  string `json:"object"`
			OwnedBy string `json:"owned_by"`
		} `json:"data"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatal(err)
	}
	if resp.Object != "list" {
		t.Fatalf("object = %q, want list", resp.Object)
	}
	if len(resp.Data) != 1 {
		t.Fatalf("models = %+v, want only qwen", resp.Data)
	}
	if resp.Data[0].ID != "qwen" || resp.Data[0].Object != "model" || resp.Data[0].OwnedBy != "self_host" {
		t.Fatalf("model = %+v, want qwen/self_host", resp.Data[0])
	}
}

func TestModelsEndpointOmitsDisabledModels(t *testing.T) {
	cfg := testProxyConfig()
	model := cfg.Models["qwen"]
	model.Disabled = true
	cfg.Models["qwen"] = model
	srv := NewServer(cfg)
	srv.workers.UpsertHeartbeat(protocol.HeartbeatRequest{
		AgentID:      "worker-a",
		Tags:         []string{"gpu-4090"},
		LlamaSwapURL: "http://worker",
		Artifacts:    map[string]string{"qwen": "ready"},
		RunningModels: []protocol.RunningModel{
			{Model: "qwen", State: "ready"},
		},
	}, time.Now())

	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	req.Header.Set("Authorization", "Bearer client-secret")
	rr := httptest.NewRecorder()

	srv.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d: %s", rr.Code, http.StatusOK, rr.Body.String())
	}
	var resp modelsResponse
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Data) != 0 {
		t.Fatalf("models = %+v, want disabled model omitted", resp.Data)
	}
}

func TestModelsEndpointListsAvailableAliases(t *testing.T) {
	for _, tc := range []struct {
		name       string
		ready      bool
		wantModels []string
	}{
		{name: "ready target", ready: true, wantModels: []string{"qwen-latest", "qwen-v2"}},
		{name: "unavailable target", ready: false, wantModels: []string{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := NewServer(aliasProxyConfig())
			registerProxyWorkerModel(t, srv, "worker-a", "http://worker", "qwen-v2", tc.ready)

			req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
			req.Header.Set("Authorization", "Bearer client-secret")
			rr := httptest.NewRecorder()

			srv.ServeHTTP(rr, req)

			if rr.Code != http.StatusOK {
				t.Fatalf("status = %d, want %d: %s", rr.Code, http.StatusOK, rr.Body.String())
			}
			var resp modelsResponse
			if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
				t.Fatal(err)
			}
			gotModels := make([]string, 0, len(resp.Data))
			for _, model := range resp.Data {
				gotModels = append(gotModels, model.ID)
			}
			if len(gotModels) != len(tc.wantModels) {
				t.Fatalf("models = %v, want %v", gotModels, tc.wantModels)
			}
			for i := range gotModels {
				if gotModels[i] != tc.wantModels[i] {
					t.Fatalf("models = %v, want %v", gotModels, tc.wantModels)
				}
			}
		})
	}
}

func TestModelsEndpointRequiresClientToken(t *testing.T) {
	srv := NewServer(testProxyConfig())
	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	rr := httptest.NewRecorder()

	srv.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusUnauthorized)
	}
}
