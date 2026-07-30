package agent

import (
	"context"
	"errors"
	"net"
	"strconv"
	"sync"
	"time"

	frpclient "github.com/fatedier/frp/client"
	frpproxy "github.com/fatedier/frp/client/proxy"
	v1 "github.com/fatedier/frp/pkg/config/v1"
)

const (
	officialFRPStatusPollInterval = 25 * time.Millisecond
	officialFRPCloseTimeout       = 500 * time.Millisecond
)

var (
	errOfficialFRPAlreadyRun = errors.New("FRP client can only run once")
	errOfficialFRPProxy      = errors.New("FRP proxy failed to start")
)

type officialFRPService interface {
	Run(context.Context) error
	GracefulClose(time.Duration)
	ProxyStatus(string) (string, bool)
}

type officialFRPServiceAdapter struct {
	service *frpclient.Service
}

func (s *officialFRPServiceAdapter) Run(ctx context.Context) error {
	return s.service.Run(ctx)
}

func (s *officialFRPServiceAdapter) GracefulClose(timeout time.Duration) {
	s.service.GracefulClose(timeout)
}

func (s *officialFRPServiceAdapter) ProxyStatus(name string) (string, bool) {
	status, ok := s.service.StatusExporter().GetProxyStatus(name)
	if !ok || status == nil {
		return "", false
	}
	return status.Phase, true
}

type officialFRPClientFactory struct {
	newService func(frpclient.ServiceOptions) (officialFRPService, error)
}

// NewOfficialFRPClientFactory returns the production embedded FRP adapter.
// Upstream FRP types remain private to this adapter.
func NewOfficialFRPClientFactory() FRPClientFactory {
	return &officialFRPClientFactory{
		newService: func(options frpclient.ServiceOptions) (officialFRPService, error) {
			service, err := frpclient.NewService(options)
			if err != nil {
				return nil, err
			}
			return &officialFRPServiceAdapter{service: service}, nil
		},
	}
}

func (f *officialFRPClientFactory) New(cfg FRPProxyConfig) (FRPClient, error) {
	host, portText, err := net.SplitHostPort(cfg.LocalAddr)
	if err != nil || host == "" {
		return nil, errors.New("invalid FRP local address")
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port < 1 || port > 65535 {
		return nil, errors.New("invalid FRP local address")
	}

	// Fail the initial login fast so the manager can fetch a rotated endpoint or
	// credential. Once logged in, FRP's controller loop still reconnects in place.
	loginFailExit := true
	common := &v1.ClientCommonConfig{
		ServerAddr: cfg.ServerAddr,
		ServerPort: cfg.ServerPort,
		Auth: v1.AuthClientConfig{
			Method: v1.AuthMethodToken,
			Token:  cfg.AuthToken,
		},
		LoginFailExit: &loginFailExit,
		Transport: v1.ClientTransportConfig{
			Protocol: "tcp",
		},
	}
	if err := common.Complete(); err != nil {
		return nil, errors.New("failed to initialize FRP client")
	}
	tcpProxy := &v1.TCPProxyConfig{
		ProxyBaseConfig: v1.ProxyBaseConfig{
			Name: cfg.ProxyName,
			Type: "tcp",
			ProxyBackend: v1.ProxyBackend{
				LocalIP:   host,
				LocalPort: port,
			},
		},
		RemotePort: cfg.RemotePort,
	}
	tcpProxy.Complete(common.User)

	service, err := f.newService(frpclient.ServiceOptions{
		Common:    common,
		ProxyCfgs: []v1.ProxyConfigurer{tcpProxy},
	})
	if err != nil {
		return nil, errors.New("failed to initialize FRP client")
	}
	return newOfficialFRPClient(service, cfg.ProxyName, officialFRPStatusPollInterval), nil
}

type officialFRPClient struct {
	service      officialFRPService
	proxyName    string
	pollInterval time.Duration

	mu          sync.Mutex
	runStarted  bool
	runFinished bool
	runCancel   context.CancelFunc
	closed      bool
	ready       bool
	runDone     chan struct{}
	closedCh    chan struct{}
}

func newOfficialFRPClient(service officialFRPService, proxyName string, pollInterval time.Duration) *officialFRPClient {
	return &officialFRPClient{
		service:      service,
		proxyName:    proxyName,
		pollInterval: pollInterval,
		runDone:      make(chan struct{}),
		closedCh:     make(chan struct{}),
	}
}

func (c *officialFRPClient) Run(ctx context.Context) error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	if c.runStarted {
		c.mu.Unlock()
		return errOfficialFRPAlreadyRun
	}
	runCtx, cancel := context.WithCancel(ctx)
	c.runStarted = true
	c.runCancel = cancel
	c.mu.Unlock()

	_ = c.service.Run(runCtx)
	c.mu.Lock()
	c.runFinished = true
	closed := c.closed
	c.runCancel = nil
	close(c.runDone)
	c.mu.Unlock()
	cancel()

	if closed || ctx.Err() != nil {
		return nil
	}
	return errFRPClientStoppedBeforeReady
}

func (c *officialFRPClient) WaitReady(ctx context.Context) error {
	interval := c.pollInterval
	if interval <= 0 {
		interval = officialFRPStatusPollInterval
	}
	timer := time.NewTimer(0)
	defer timer.Stop()

	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-c.closedCh:
			return errFRPClientStoppedBeforeReady
		case <-c.runDone:
			return errFRPClientStoppedBeforeReady
		case <-timer.C:
		}

		c.mu.Lock()
		started := c.runStarted
		finished := c.runFinished
		c.mu.Unlock()
		if finished {
			return errFRPClientStoppedBeforeReady
		}
		if started {
			phase, ok := c.service.ProxyStatus(c.proxyName)
			if ok {
				switch phase {
				case frpproxy.ProxyPhaseRunning:
					if err := ctx.Err(); err != nil {
						return err
					}
					c.mu.Lock()
					if c.closed || c.runFinished {
						c.mu.Unlock()
						return errFRPClientStoppedBeforeReady
					}
					c.ready = true
					c.mu.Unlock()
					return nil
				case frpproxy.ProxyPhaseStartErr, frpproxy.ProxyPhaseClosed:
					return errOfficialFRPProxy
				}
			}
		}
		timer.Reset(interval)
	}
}

func (c *officialFRPClient) Close() error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	c.closed = true
	close(c.closedCh)
	ready := c.ready
	cancel := c.runCancel
	c.mu.Unlock()

	if ready {
		c.service.GracefulClose(officialFRPCloseTimeout)
	}
	if cancel != nil {
		cancel()
	}
	return nil
}

var _ FRPClientFactory = (*officialFRPClientFactory)(nil)
var _ FRPClient = (*officialFRPClient)(nil)
var _ officialFRPService = (*officialFRPServiceAdapter)(nil)
