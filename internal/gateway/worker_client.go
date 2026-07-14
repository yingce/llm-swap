package gateway

import "llm-swap/internal/config"

func (s *Server) llamaSwapClientForWorker(worker Worker, cfg config.GatewayConfig) LlamaSwapClient {
	client := LlamaSwapClient{BearerToken: cfg.Tokens.LlamaSwap}
	if s == nil || s.tunnels == nil {
		return client
	}
	tunnel, ok := s.tunnels.Get(worker.ID)
	if !ok {
		return client
	}
	client.Tunnel = tunnel
	return client
}

func (s *Server) tunnelForWorker(workerID string) *AgentTunnel {
	if s == nil || s.tunnels == nil {
		return nil
	}
	tunnel, ok := s.tunnels.Get(workerID)
	if !ok {
		return nil
	}
	return tunnel
}
