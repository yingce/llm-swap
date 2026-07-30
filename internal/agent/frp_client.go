package agent

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"
)

var errFRPClientStoppedBeforeReady = errors.New("FRP client stopped before proxy became ready")

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
	const (
		proxyNamePrefix = "llmswap-"
		proxyNameLimit  = 64
	)
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
	digest := sha256.Sum256([]byte(agentID))
	suffix := fmt.Sprintf("-%x-g%d", digest[:5], generation)
	identityLimit := proxyNameLimit - len(proxyNamePrefix) - len(suffix)
	if len(identity) > identityLimit {
		identity = strings.TrimRight(identity[:identityLimit], "-")
	}
	if identity == "" {
		identity = "agent"
	}
	return proxyNamePrefix + identity + suffix
}
