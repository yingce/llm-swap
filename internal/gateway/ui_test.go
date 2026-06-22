package gateway

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"llm-swap/internal/config"
	"llm-swap/internal/protocol"
)

func TestUIStatusEndpointSummarizesWorkersModelsAndEvents(t *testing.T) {
	srv := NewServer(testGatewayConfig())

	postHeartbeat(t, srv, protocol.HeartbeatRequest{
		AgentID:       "gpu-01",
		Tags:          []string{"gpu-4090"},
		LlamaSwapURL:  "http://worker",
		Artifacts:     map[string]string{"qwen": "ready"},
		Capacity:      config.WorkerDefaults{MaxConcurrency: 2, MaxQueue: 4},
		RunningModels: []protocol.RunningModel{{Model: "qwen", State: "ready"}},
		Events: []protocol.AgentEvent{
			{
				Time:            time.Unix(100, 0).UTC(),
				Event:           "artifact_download_progress",
				Model:           "qwen",
				Object:          "models/qwen.tar.gz",
				DownloadedBytes: 50,
				TotalBytes:      100,
				Percent:         50,
			},
		},
	})

	req := httptest.NewRequest(http.MethodGet, "/ui/status", nil)
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d: %s", rr.Code, http.StatusOK, rr.Body.String())
	}
	var status uiStatusResponse
	if err := json.NewDecoder(rr.Body).Decode(&status); err != nil {
		t.Fatalf("decode status: %v", err)
	}
	if status.Summary.TotalWorkers != 1 || status.Summary.HealthyWorkers != 1 {
		t.Fatalf("summary = %+v, want one healthy worker", status.Summary)
	}
	model, ok := findUIModel(status.Models, "qwen")
	if !ok {
		t.Fatalf("models = %+v, want qwen", status.Models)
	}
	if model.Name != "qwen" || model.ReadyWorkers != 1 || model.RunningWorkers != 1 || !model.Available {
		t.Fatalf("model status = %+v, want qwen ready/running/available", model)
	}
	if len(status.Workers) != 1 || status.Workers[0].ID != "gpu-01" || status.Workers[0].Health != "healthy" {
		t.Fatalf("workers = %+v, want healthy gpu-01", status.Workers)
	}
	if len(status.Events) != 1 || status.Events[0].WorkerID != "gpu-01" || status.Events[0].Event != "artifact_download_progress" {
		t.Fatalf("events = %+v, want cached progress event", status.Events)
	}
}

func findUIModel(models []uiModelStatus, name string) (uiModelStatus, bool) {
	for _, model := range models {
		if model.Name == name {
			return model, true
		}
	}
	return uiModelStatus{}, false
}

func TestUIPageServesDashboardHTML(t *testing.T) {
	srv := NewServer(testGatewayConfig())
	req := httptest.NewRequest(http.MethodGet, "/ui", nil)
	rr := httptest.NewRecorder()

	srv.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d: %s", rr.Code, http.StatusOK, rr.Body.String())
	}
	if got := rr.Header().Get("Content-Type"); !strings.Contains(got, "text/html") {
		t.Fatalf("content-type = %q, want text/html", got)
	}
	body := rr.Body.String()
	for _, want := range []string{"LLM Swap Gateway", "/ui/status", "Models", "Workers", "Recent worker events", "worker-card-grid", "model-table", "breakable"} {
		if !strings.Contains(body, want) {
			t.Fatalf("body missing %q:\n%s", want, body)
		}
	}
}

func TestUIStatusEndpointUsesEmptyArraysInsteadOfNull(t *testing.T) {
	srv := NewServer(testGatewayConfig())
	req := httptest.NewRequest(http.MethodGet, "/ui/status", nil)
	rr := httptest.NewRecorder()

	srv.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d: %s", rr.Code, http.StatusOK, rr.Body.String())
	}
	body := rr.Body.String()
	for _, forbidden := range []string{
		`"events":null`,
		`"workers":null`,
		`"models":null`,
		`"worker_statuses":null`,
	} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("body contains %s:\n%s", forbidden, body)
		}
	}
}
