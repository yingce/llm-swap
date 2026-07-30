package gateway

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"

	"llm-swap/internal/config"
)

type transportGenerationInput struct {
	Type            string `json:"type"`
	ServerAddr      string `json:"server_addr"`
	ServerPort      int    `json:"server_port"`
	AuthToken       string `json:"auth_token"`
	PortStart       int    `json:"port_start"`
	PortEnd         int    `json:"port_end"`
	LeaseTTLSeconds int    `json:"lease_ttl_seconds"`
	LlamaSwapToken  string `json:"llama_swap_token"`
}

func transportConfigGeneration(cfg config.GatewayConfig) (uint64, error) {
	llamaSwapToken := cfg.Tokens.LlamaSwap
	if llamaSwapToken == "" {
		llamaSwapToken = cfg.Tokens.Agent
	}
	payload, err := json.Marshal(transportGenerationInput{
		Type:            cfg.Transport.Type,
		ServerAddr:      cfg.Transport.FRP.ServerAddr,
		ServerPort:      cfg.Transport.FRP.ServerPort,
		AuthToken:       cfg.Transport.FRP.AuthToken,
		PortStart:       cfg.Transport.FRP.PortStart,
		PortEnd:         cfg.Transport.FRP.PortEnd,
		LeaseTTLSeconds: cfg.Transport.FRP.LeaseTTLSeconds,
		LlamaSwapToken:  llamaSwapToken,
	})
	if err != nil {
		return 0, err
	}
	digest := sha256.Sum256(payload)
	generation := binary.BigEndian.Uint64(digest[:8])
	if generation == 0 {
		generation = 1
	}
	return generation, nil
}
