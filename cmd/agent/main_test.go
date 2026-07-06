package main

import (
	"strings"
	"testing"

	"llm-swap/internal/buildinfo"
	"llm-swap/internal/protocol"
)

func TestAgentVersionTextIncludesSourceVersionAndProtocol(t *testing.T) {
	text := agentVersionText(buildinfo.Current(protocol.AgentProtocolVersion))
	for _, want := range []string{"agent_version=2026.07.06.2", "agent_protocol=2"} {
		if !strings.Contains(text, want) {
			t.Fatalf("version text %q missing %q", text, want)
		}
	}
}
