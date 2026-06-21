package protocol

import (
	"encoding/json"
	"strings"
	"testing"

	"llm-swap/internal/config"
)

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
