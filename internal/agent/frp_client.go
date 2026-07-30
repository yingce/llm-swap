package agent

import (
	"context"
	"fmt"
	"strings"
)

// FRPProxyConfig is the repository-owned boundary consumed by an FRP client
// adapter. It intentionally contains no formatting helpers so credentials are
// not accidentally included in logs.
type FRPProxyConfig struct {
	ServerAddr string
	ServerPort int
	AuthToken  string
	ProxyName  string
	LocalAddr  string
	RemotePort int
}

type FRPClient interface {
	Run(context.Context) error
	WaitReady(context.Context) error
	Close() error
}

type FRPClientFactory interface {
	New(FRPProxyConfig) (FRPClient, error)
}

func frpProxyName(agentID string, generation uint64) string {
	var sanitized strings.Builder
	separator := false
	for _, r := range strings.ToLower(strings.TrimSpace(agentID)) {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '_' || r == '-' {
			if separator && sanitized.Len() > 0 {
				sanitized.WriteByte('-')
			}
			separator = false
			sanitized.WriteRune(r)
			continue
		}
		separator = true
	}
	identity := strings.Trim(sanitized.String(), "-")
	if identity == "" {
		identity = "agent"
	}
	return fmt.Sprintf("llmswap-%s-g%d", identity, generation)
}
