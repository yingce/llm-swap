package protocol

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"llm-swap/internal/config"
)

func TestTransportProtocolJSONContract(t *testing.T) {
	expiresAt := time.Date(2026, time.July, 30, 12, 34, 56, 123000000, time.UTC)

	tests := []struct {
		name  string
		value any
		want  map[string]any
	}{
		{
			name: "encrypted bootstrap",
			value: EncryptedTransportBootstrap{
				Generation: 42,
				Nonce:      "nonce-value",
				Ciphertext: "ciphertext-value",
			},
			want: map[string]any{
				"generation": 42.0,
				"nonce":      "nonce-value",
				"ciphertext": "ciphertext-value",
			},
		},
		{
			name: "lease request",
			value: TransportLeaseRequest{
				AgentID:      "worker-gpu0",
				Generation:   42,
				LeaseID:      "lease-1",
				Release:      true,
				ExcludeSlots: []int{2, 5},
			},
			want: map[string]any{
				"agent_id":      "worker-gpu0",
				"generation":    42.0,
				"lease_id":      "lease-1",
				"release":       true,
				"exclude_slots": []any{2.0, 5.0},
			},
		},
		{
			name: "lease response",
			value: TransportLeaseResponse{
				LeaseID:    "lease-1",
				Slot:       3,
				RemotePort: 2003,
				Generation: 42,
				ExpiresAt:  expiresAt,
			},
			want: map[string]any{
				"lease_id":    "lease-1",
				"slot":        3.0,
				"remote_port": 2003.0,
				"generation":  42.0,
				"expires_at":  "2026-07-30T12:34:56.123Z",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data, err := json.Marshal(tt.value)
			if err != nil {
				t.Fatal(err)
			}
			var got map[string]any
			if err := json.Unmarshal(data, &got); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("json contract mismatch\n got: %#v\nwant: %#v", got, tt.want)
			}
		})
	}

	transport := EncryptedTransportBootstrap{Generation: 42, Nonce: "n", Ciphertext: "c"}
	configData, err := json.Marshal(AgentConfigResponse{
		Transport: &transport,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(configData), `"transport":{"generation":42,"nonce":"n","ciphertext":"c"}`) {
		t.Fatalf("agent config response does not expose transport envelope: %s", configData)
	}
	legacyConfigData, err := json.Marshal(AgentConfigResponse{})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(legacyConfigData), `"transport"`) {
		t.Fatalf("legacy agent config response contains an empty transport envelope: %s", legacyConfigData)
	}

	heartbeatData, err := json.Marshal(HeartbeatRequest{
		TransportLeaseID:    "lease-1",
		TransportGeneration: 42,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"transport_lease_id":"lease-1"`, `"transport_generation":42`} {
		if !strings.Contains(string(heartbeatData), want) {
			t.Fatalf("heartbeat json %s missing %s", heartbeatData, want)
		}
	}
}

func TestAgentConfigResponseJSONUsesSnakeCaseConfigFields(t *testing.T) {
	resp := AgentConfigResponse{
		OSS: config.OSSConfig{BaseURL: "https://oss.example.com"},
		Models: map[string]config.Model{
			"qwen": {
				MaxConcurrency: 2,
				QueueTimeoutMS: 30000,
				Artifact: config.Artifact{
					Object:    "qwen.tar.gz",
					Kind:      "tar_gz",
					CRC64ECMA: "123",
				},
			},
		},
		TagPolicy: AgentTagPolicy{
			WorkerDefaults: config.WorkerDefaults{MaxConcurrency: 2, MaxQueue: 4},
		},
	}

	data, err := json.Marshal(resp)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, want := range []string{"base_url", "max_concurrency", "queue_timeout_ms", "crc64ecma", "worker_defaults"} {
		if !strings.Contains(text, want) {
			t.Fatalf("json %s missing %s", text, want)
		}
	}
	for _, bad := range []string{"BaseURL", "MaxConcurrency", "QueueTimeoutMS", "CRC64ECMA", "WorkerDefaults"} {
		if strings.Contains(text, bad) {
			t.Fatalf("json %s contains Go field name %s", text, bad)
		}
	}
}
