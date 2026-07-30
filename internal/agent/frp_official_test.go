package agent

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	frpclient "github.com/fatedier/frp/client"
	frpproxy "github.com/fatedier/frp/client/proxy"
	v1 "github.com/fatedier/frp/pkg/config/v1"
)

func TestOfficialFRPFactoryMapsTCPOptions(t *testing.T) {
	t.Setenv("http_proxy", "")

	var got frpclient.ServiceOptions
	factory := &officialFRPClientFactory{
		newService: func(options frpclient.ServiceOptions) (officialFRPService, error) {
			got = options
			return newFakeOfficialFRPService(), nil
		},
	}
	cfg := FRPProxyConfig{
		ServerAddr: "frps.example.test",
		ServerPort: 7000,
		AuthToken:  "do-not-print-this-token",
		ProxyName:  "llmswap-worker-7",
		LocalAddr:  "127.0.0.1:6006",
		RemotePort: 2007,
	}
	if _, err := factory.New(cfg); err != nil {
		t.Fatalf("New() error = %v", err)
	}

	if got.Common == nil {
		t.Fatal("Common config is nil")
	}
	if got.Common.ServerAddr != cfg.ServerAddr || got.Common.ServerPort != cfg.ServerPort {
		t.Fatalf("server = %s:%d, want %s:%d", got.Common.ServerAddr, got.Common.ServerPort, cfg.ServerAddr, cfg.ServerPort)
	}
	if got.Common.Auth.Method != v1.AuthMethodToken || got.Common.Auth.Token != cfg.AuthToken {
		t.Fatalf("auth mapping mismatch: method=%q token_matches=%t", got.Common.Auth.Method, got.Common.Auth.Token == cfg.AuthToken)
	}
	if got.Common.Transport.Protocol != "tcp" {
		t.Fatalf("transport protocol = %q, want tcp", got.Common.Transport.Protocol)
	}
	if got.Common.Transport.ProxyURL != "" {
		t.Fatalf("transport proxy URL = %q, want empty", got.Common.Transport.ProxyURL)
	}
	if got.Common.LoginFailExit == nil || *got.Common.LoginFailExit {
		t.Fatalf("LoginFailExit = %v, want false", got.Common.LoginFailExit)
	}
	if got.Common.WebServer.Port != 0 {
		t.Fatalf("web server port = %d, want disabled", got.Common.WebServer.Port)
	}
	if got.Common.Transport.TCPMux == nil || !*got.Common.Transport.TCPMux {
		t.Fatalf("TCPMux = %v, want completed true default", got.Common.Transport.TCPMux)
	}
	if got.Common.Transport.TLS.Enable == nil || !*got.Common.Transport.TLS.Enable {
		t.Fatalf("TLS enable = %v, want completed true default", got.Common.Transport.TLS.Enable)
	}
	if len(got.ProxyCfgs) != 1 {
		t.Fatalf("proxy count = %d, want 1", len(got.ProxyCfgs))
	}
	tcpProxy, ok := got.ProxyCfgs[0].(*v1.TCPProxyConfig)
	if !ok {
		t.Fatalf("proxy type = %T, want *v1.TCPProxyConfig", got.ProxyCfgs[0])
	}
	if tcpProxy.Name != cfg.ProxyName || tcpProxy.Type != "tcp" {
		t.Fatalf("proxy identity = %q/%q, want %q/tcp", tcpProxy.Name, tcpProxy.Type, cfg.ProxyName)
	}
	if tcpProxy.LocalIP != "127.0.0.1" || tcpProxy.LocalPort != 6006 || tcpProxy.RemotePort != cfg.RemotePort {
		t.Fatalf("proxy route = %s:%d -> %d", tcpProxy.LocalIP, tcpProxy.LocalPort, tcpProxy.RemotePort)
	}
	if tcpProxy.Transport.BandwidthLimitMode != "client" {
		t.Fatalf("proxy config was not completed: bandwidth limit mode = %q", tcpProxy.Transport.BandwidthLimitMode)
	}
}

func TestOfficialFRPFactoryRejectsInvalidLocalAddressWithoutLeakingToken(t *testing.T) {
	secret := "secret-that-must-not-leak"
	factory := NewOfficialFRPClientFactory()
	_, err := factory.New(FRPProxyConfig{
		ServerAddr: "frps.example.test",
		ServerPort: 7000,
		AuthToken:  secret,
		ProxyName:  "worker",
		LocalAddr:  "missing-port",
		RemotePort: 2000,
	})
	if err == nil {
		t.Fatal("New() error = nil, want invalid local address error")
	}
	if strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), "missing-port") {
		t.Fatalf("error leaked configuration: %q", err)
	}
}

func TestOfficialFRPFactorySanitizesServiceErrors(t *testing.T) {
	secret := "upstream-error-with-secret"
	factory := &officialFRPClientFactory{
		newService: func(frpclient.ServiceOptions) (officialFRPService, error) {
			return nil, errors.New(secret)
		},
	}
	_, err := factory.New(FRPProxyConfig{
		ServerAddr: "frps.example.test",
		ServerPort: 7000,
		AuthToken:  "auth-secret",
		ProxyName:  "worker",
		LocalAddr:  "127.0.0.1:6006",
		RemotePort: 2000,
	})
	if err == nil {
		t.Fatal("New() error = nil, want service error")
	}
	if strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), "auth-secret") {
		t.Fatalf("New() leaked upstream details: %q", err)
	}
}

func TestOfficialFRPClientWaitsForExactProxyToRun(t *testing.T) {
	service := newFakeOfficialFRPService()
	client := newOfficialFRPClient(service, "exact-proxy", time.Millisecond)
	runResult := startOfficialFRPClient(t, client, service)

	readyResult := make(chan error, 1)
	go func() { readyResult <- client.WaitReady(context.Background()) }()
	select {
	case err := <-readyResult:
		t.Fatalf("WaitReady() returned while proxy was new: %v", err)
	case <-time.After(10 * time.Millisecond):
	}
	service.setPhase(frpproxy.ProxyPhaseWaitStart)
	select {
	case err := <-readyResult:
		t.Fatalf("WaitReady() returned while proxy was waiting: %v", err)
	case <-time.After(10 * time.Millisecond):
	}

	service.setPhase(frpproxy.ProxyPhaseRunning)
	select {
	case err := <-readyResult:
		if err != nil {
			t.Fatalf("WaitReady() error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("WaitReady() did not observe running proxy")
	}
	if got := service.lastStatusName(); got != "exact-proxy" {
		t.Fatalf("status lookup name = %q, want exact-proxy", got)
	}

	if err := client.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	assertRunStops(t, runResult)
}

func TestOfficialFRPClientReturnsGenericClosedError(t *testing.T) {
	service := newFakeOfficialFRPService()
	service.setPhase(frpproxy.ProxyPhaseClosed)
	client := newOfficialFRPClient(service, "proxy", time.Millisecond)
	runResult := startOfficialFRPClient(t, client, service)

	if err := client.WaitReady(context.Background()); !errors.Is(err, errOfficialFRPProxy) {
		t.Fatalf("WaitReady() error = %v, want generic proxy error", err)
	}
	_ = client.Close()
	assertRunStops(t, runResult)
}

func TestOfficialFRPClientReturnsGenericStartError(t *testing.T) {
	service := newFakeOfficialFRPService()
	service.setPhase(frpproxy.ProxyPhaseStartErr)
	client := newOfficialFRPClient(service, "proxy", time.Millisecond)
	runResult := startOfficialFRPClient(t, client, service)

	err := client.WaitReady(context.Background())
	if err == nil {
		t.Fatal("WaitReady() error = nil, want start error")
	}
	if strings.Contains(err.Error(), "token") || strings.Contains(err.Error(), "secret") {
		t.Fatalf("WaitReady() error exposed FRP details: %q", err)
	}
	_ = client.Close()
	assertRunStops(t, runResult)
}

func TestOfficialFRPClientWaitReadyHonorsContext(t *testing.T) {
	service := newFakeOfficialFRPService()
	client := newOfficialFRPClient(service, "proxy", time.Millisecond)
	runResult := startOfficialFRPClient(t, client, service)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := client.WaitReady(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("WaitReady() error = %v, want context.Canceled", err)
	}
	_ = client.Close()
	assertRunStops(t, runResult)
}

func TestOfficialFRPClientCanceledContextWinsOverRunningStatus(t *testing.T) {
	service := newFakeOfficialFRPService()
	service.setPhase(frpproxy.ProxyPhaseRunning)
	client := newOfficialFRPClient(service, "proxy", time.Microsecond)
	runResult := startOfficialFRPClient(t, client, service)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for i := 0; i < 100; i++ {
		if err := client.WaitReady(ctx); !errors.Is(err, context.Canceled) {
			t.Fatalf("iteration %d: WaitReady() error = %v, want context.Canceled", i, err)
		}
	}
	_ = client.Close()
	assertRunStops(t, runResult)
}

func TestOfficialFRPClientCloseBeforeRunIsSafe(t *testing.T) {
	service := newFakeOfficialFRPService()
	client := newOfficialFRPClient(service, "proxy", time.Millisecond)

	if err := client.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if err := client.Close(); err != nil {
		t.Fatalf("second Close() error = %v", err)
	}
	if err := client.Run(context.Background()); err != nil {
		t.Fatalf("Run() after Close() error = %v", err)
	}
	if got := service.closeCallCount(); got != 0 {
		t.Fatalf("GracefulClose calls = %d, want 0 before Run", got)
	}
}

func TestOfficialFRPClientCloseAfterReadyIsIdempotent(t *testing.T) {
	service := newFakeOfficialFRPService()
	service.setPhase(frpproxy.ProxyPhaseRunning)
	client := newOfficialFRPClient(service, "proxy", time.Millisecond)
	runResult := startOfficialFRPClient(t, client, service)
	if err := client.WaitReady(context.Background()); err != nil {
		t.Fatalf("WaitReady() error = %v", err)
	}

	if err := client.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if err := client.Close(); err != nil {
		t.Fatalf("second Close() error = %v", err)
	}
	assertRunStops(t, runResult)
	if got := service.closeCallCount(); got != 1 {
		t.Fatalf("GracefulClose calls = %d, want 1", got)
	}
}

func TestOfficialFRPClientCloseBeforeReadySkipsGracefulClose(t *testing.T) {
	service := newFakeOfficialFRPService()
	client := newOfficialFRPClient(service, "proxy", time.Millisecond)
	runResult := startOfficialFRPClient(t, client, service)

	if err := client.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	assertRunStops(t, runResult)
	if got := service.closeCallCount(); got != 0 {
		t.Fatalf("GracefulClose calls = %d, want 0 before readiness", got)
	}
}

func TestOfficialFRPClientRunExitBeforeReady(t *testing.T) {
	service := newFakeOfficialFRPService()
	client := newOfficialFRPClient(service, "proxy", time.Millisecond)
	runResult := startOfficialFRPClient(t, client, service)
	service.releaseRun(nil)
	if err := <-runResult; err == nil {
		t.Fatal("Run() error = nil for unexpected service exit")
	}
	if err := client.WaitReady(context.Background()); err == nil {
		t.Fatal("WaitReady() error = nil after service exit")
	}
}

func TestOfficialFRPClientSanitizesRunError(t *testing.T) {
	service := newFakeOfficialFRPService()
	client := newOfficialFRPClient(service, "proxy", time.Millisecond)
	runResult := startOfficialFRPClient(t, client, service)
	service.releaseRun(errors.New("upstream failure with auth-secret"))
	err := <-runResult
	if err == nil {
		t.Fatal("Run() error = nil, want unexpected exit error")
	}
	if strings.Contains(err.Error(), "upstream") || strings.Contains(err.Error(), "auth-secret") {
		t.Fatalf("Run() leaked upstream details: %q", err)
	}
}

func TestOfficialFRPClientRunOnlyOnce(t *testing.T) {
	service := newFakeOfficialFRPService()
	client := newOfficialFRPClient(service, "proxy", time.Millisecond)
	runResult := startOfficialFRPClient(t, client, service)
	if err := client.Run(context.Background()); !errors.Is(err, errOfficialFRPAlreadyRun) {
		t.Fatalf("second Run() error = %v, want single-run error", err)
	}
	_ = client.Close()
	assertRunStops(t, runResult)
}

func TestOfficialFRPClientRunAndCloseConcurrent(t *testing.T) {
	for i := 0; i < 100; i++ {
		service := newFakeOfficialFRPService()
		client := newOfficialFRPClient(service, "proxy", time.Microsecond)
		runResult := make(chan error, 1)
		go func() { runResult <- client.Run(context.Background()) }()
		if err := client.Close(); err != nil {
			t.Fatalf("iteration %d: Close() error = %v", i, err)
		}
		select {
		case <-runResult:
		case <-time.After(time.Second):
			t.Fatalf("iteration %d: concurrent Run/Close hung", i)
		}
	}
}

func startOfficialFRPClient(t *testing.T, client FRPClient, service *fakeOfficialFRPService) <-chan error {
	t.Helper()
	result := make(chan error, 1)
	go func() { result <- client.Run(context.Background()) }()
	select {
	case <-service.runStarted:
	case <-time.After(time.Second):
		t.Fatal("service Run() did not start")
	}
	return result
}

func assertRunStops(t *testing.T, result <-chan error) {
	t.Helper()
	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("Run() error during cooperative close = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Run() did not stop")
	}
}

type fakeOfficialFRPService struct {
	mu            sync.Mutex
	phase         string
	statusPresent bool
	runErr        error
	runStarted    chan struct{}
	runRelease    chan struct{}
	startOnce     sync.Once
	releaseOnce   sync.Once
	closeCalls    int
	statusName    string
}

func newFakeOfficialFRPService() *fakeOfficialFRPService {
	return &fakeOfficialFRPService{
		phase:         frpproxy.ProxyPhaseNew,
		statusPresent: true,
		runStarted:    make(chan struct{}),
		runRelease:    make(chan struct{}),
	}
}

func (f *fakeOfficialFRPService) Run(ctx context.Context) error {
	f.startOnce.Do(func() { close(f.runStarted) })
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-f.runRelease:
		f.mu.Lock()
		err := f.runErr
		f.mu.Unlock()
		return err
	}
}

func (f *fakeOfficialFRPService) GracefulClose(time.Duration) {
	f.mu.Lock()
	f.closeCalls++
	f.mu.Unlock()
}

func (f *fakeOfficialFRPService) ProxyStatus(name string) (string, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.statusName = name
	return f.phase, f.statusPresent
}

func (f *fakeOfficialFRPService) setPhase(phase string) {
	f.mu.Lock()
	f.phase = phase
	f.mu.Unlock()
}

func (f *fakeOfficialFRPService) releaseRun(err error) {
	f.mu.Lock()
	f.runErr = err
	f.mu.Unlock()
	f.releaseOnce.Do(func() { close(f.runRelease) })
}

func (f *fakeOfficialFRPService) lastStatusName() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.statusName
}

func (f *fakeOfficialFRPService) closeCallCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.closeCalls
}
