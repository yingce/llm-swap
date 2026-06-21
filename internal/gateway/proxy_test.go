package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"llm-swap/internal/config"
	"llm-swap/internal/protocol"
	"llm-swap/internal/testutil"
)

func TestLlamaSwapClientUnloadPostsModelWithBearerToken(t *testing.T) {
	var gotPath string
	var gotAuth string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusNoContent)
	}))
	defer upstream.Close()

	client := LlamaSwapClient{BearerToken: "llama-secret"}

	if err := client.Unload(context.Background(), upstream.URL, "qwen"); err != nil {
		t.Fatalf("Unload returned error: %v", err)
	}
	if gotPath != "/api/models/unload/qwen" {
		t.Fatalf("path = %q, want unload path", gotPath)
	}
	if gotAuth != "Bearer llama-secret" {
		t.Fatalf("authorization = %q, want bearer token", gotAuth)
	}
}

func TestLlamaSwapClientUnloadReturnsHTTPStatusError(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusTeapot)
	}))
	defer upstream.Close()

	client := LlamaSwapClient{}

	err := client.Unload(context.Background(), upstream.URL, "qwen")
	var statusErr HTTPStatusError
	if !errors.As(err, &statusErr) {
		t.Fatalf("error = %T %v, want HTTPStatusError", err, err)
	}
	if statusErr.StatusCode != http.StatusTeapot {
		t.Fatalf("status = %d, want %d", statusErr.StatusCode, http.StatusTeapot)
	}
}

func TestExtractModel(t *testing.T) {
	if got := ExtractModel([]byte(`{"model":"qwen","messages":[]}`)); got != "qwen" {
		t.Fatalf("ExtractModel = %q, want qwen", got)
	}
	if got := ExtractModel([]byte(`{"messages":[]}`)); got != "" {
		t.Fatalf("ExtractModel without model = %q, want empty", got)
	}
	if got := ExtractModel([]byte(`{`)); got != "" {
		t.Fatalf("ExtractModel invalid JSON = %q, want empty", got)
	}
}

func TestProxyMissingModelReturnsOpenAIErrorWithoutAccounting(t *testing.T) {
	srv := NewServer(testProxyConfig())
	req := proxyRequest(`{"messages":[]}`)
	rr := httptest.NewRecorder()

	srv.ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d: %s", rr.Code, http.StatusBadRequest, rr.Body.String())
	}
	assertOpenAIErrorCode(t, rr.Body.Bytes(), "missing_model")
	if got := len(srv.accounting.RequestSnapshot()); got != 0 {
		t.Fatalf("accounting snapshot length = %d, want 0", got)
	}
}

func TestProxyUnknownModelReturnsOpenAIError(t *testing.T) {
	srv := NewServer(testProxyConfig())
	req := proxyRequest(`{"model":"missing","messages":[]}`)
	rr := httptest.NewRecorder()

	srv.ServeHTTP(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d: %s", rr.Code, http.StatusNotFound, rr.Body.String())
	}
	assertOpenAIErrorCode(t, rr.Body.Bytes(), "model_not_available")
}

func TestProxyUpstream400IsNotRetriedAndResponseForwarded(t *testing.T) {
	var firstRequests atomic.Int32
	var secondRequests atomic.Int32
	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		firstRequests.Add(1)
		w.Header().Set("X-Upstream", "first")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"bad request"}`))
	}))
	defer first.Close()
	second := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		secondRequests.Add(1)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer second.Close()

	srv := NewServer(testProxyConfig())
	registerProxyWorker(t, srv, "first", first.URL, true)
	registerProxyWorker(t, srv, "second", second.URL, false)

	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, proxyRequest(`{"model":"qwen","messages":[]}`))

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusBadRequest)
	}
	if rr.Header().Get("X-Upstream") != "first" {
		t.Fatalf("X-Upstream = %q, want first", rr.Header().Get("X-Upstream"))
	}
	if rr.Body.String() != `{"error":"bad request"}` {
		t.Fatalf("body = %q, want upstream body", rr.Body.String())
	}
	if firstRequests.Load() != 1 {
		t.Fatalf("first requests = %d, want 1", firstRequests.Load())
	}
	if secondRequests.Load() != 0 {
		t.Fatalf("second requests = %d, want 0", secondRequests.Load())
	}
	if got := len(srv.accounting.RequestSnapshot()); got != 0 {
		t.Fatalf("accounting snapshot length = %d, want 0", got)
	}
}

func TestProxyRetriesDifferentWorkerBeforeHeaders(t *testing.T) {
	var firstRequests atomic.Int32
	var secondRequests atomic.Int32
	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		firstRequests.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer first.Close()
	second := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		secondRequests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl-ok","choices":[]}`))
	}))
	defer second.Close()

	srv := NewServer(testProxyConfig())
	registerProxyWorker(t, srv, "first", first.URL, true)
	registerProxyWorker(t, srv, "second", second.URL, false)

	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, proxyRequest(`{"model":"qwen","messages":[]}`))

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d: %s", rr.Code, http.StatusOK, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "chatcmpl-ok") {
		t.Fatalf("body = %q, want good worker response", rr.Body.String())
	}
	if firstRequests.Load() != 1 {
		t.Fatalf("first requests = %d, want 1", firstRequests.Load())
	}
	if secondRequests.Load() != 1 {
		t.Fatalf("second requests = %d, want 1", secondRequests.Load())
	}
	if got := len(srv.accounting.RequestSnapshot()); got != 0 {
		t.Fatalf("accounting snapshot length = %d, want 0", got)
	}
}

func TestProxyAllWorkersReturn503ReportsUpstreamRetryExhausted(t *testing.T) {
	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer first.Close()
	second := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer second.Close()

	srv := NewServer(testProxyConfig())
	registerProxyWorker(t, srv, "first", first.URL, true)
	registerProxyWorker(t, srv, "second", second.URL, false)

	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, proxyRequest(`{"model":"qwen","messages":[]}`))

	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d: %s", rr.Code, http.StatusServiceUnavailable, rr.Body.String())
	}
	assertOpenAIErrorCode(t, rr.Body.Bytes(), "upstream_retry_exhausted")
	assertNotOpenAIErrorCode(t, rr.Body.Bytes(), "no_healthy_worker")
}

func TestProxyAllWorkersReturn429PreservesTooManyRequests(t *testing.T) {
	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer first.Close()
	second := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer second.Close()

	srv := NewServer(testProxyConfig())
	registerProxyWorker(t, srv, "first", first.URL, true)
	registerProxyWorker(t, srv, "second", second.URL, false)

	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, proxyRequest(`{"model":"qwen","messages":[]}`))

	if rr.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want %d: %s", rr.Code, http.StatusTooManyRequests, rr.Body.String())
	}
	assertOpenAIErrorCode(t, rr.Body.Bytes(), "upstream_retry_exhausted")
	assertNotOpenAIErrorCode(t, rr.Body.Bytes(), "no_healthy_worker")
}

func TestProxyMalformedWorkerURLReportsWorkerUnavailable(t *testing.T) {
	srv := NewServer(testProxyConfig())
	registerProxyWorker(t, srv, "broken", "", true)

	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, proxyRequest(`{"model":"qwen","messages":[]}`))

	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d: %s", rr.Code, http.StatusServiceUnavailable, rr.Body.String())
	}
	assertOpenAIErrorCode(t, rr.Body.Bytes(), "worker_unavailable")
	assertNotOpenAIErrorCode(t, rr.Body.Bytes(), "no_healthy_worker")
}

func TestProxyStripsRequestHeadersNamedByConnection(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("X-Client-Hop"); got != "" {
			t.Fatalf("X-Client-Hop reached upstream: %q", got)
		}
		if got := r.Header.Get("Connection"); got != "" {
			t.Fatalf("Connection reached upstream: %q", got)
		}
		_, _ = w.Write([]byte(`{"choices":[]}`))
	}))
	defer upstream.Close()

	srv := NewServer(testProxyConfig())
	registerProxyWorker(t, srv, "worker", upstream.URL, true)
	req := proxyRequest(`{"model":"qwen","messages":[]}`)
	req.Header.Set("Connection", "X-Client-Hop")
	req.Header.Set("X-Client-Hop", "secret")
	rr := httptest.NewRecorder()

	srv.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d: %s", rr.Code, http.StatusOK, rr.Body.String())
	}
}

func TestProxyStripsResponseHeadersNamedByConnection(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Connection", "X-Upstream-Hop")
		w.Header().Set("X-Upstream-Hop", "secret")
		_, _ = w.Write([]byte(`{"choices":[]}`))
	}))
	defer upstream.Close()

	srv := NewServer(testProxyConfig())
	registerProxyWorker(t, srv, "worker", upstream.URL, true)
	rr := httptest.NewRecorder()

	srv.ServeHTTP(rr, proxyRequest(`{"model":"qwen","messages":[]}`))

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d: %s", rr.Code, http.StatusOK, rr.Body.String())
	}
	if got := rr.Header().Get("X-Upstream-Hop"); got != "" {
		t.Fatalf("X-Upstream-Hop = %q, want stripped", got)
	}
	if got := rr.Header().Get("Connection"); got != "" {
		t.Fatalf("Connection = %q, want stripped", got)
	}
}

func TestProxyStreamingKeepsWorkerAndAccountingActiveUntilBodyCopyFinishes(t *testing.T) {
	started := make(chan struct{})
	releaseStream := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("data: first\n\n"))
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		close(started)
		<-releaseStream
		_, _ = w.Write([]byte("data: done\n\n"))
	}))
	defer upstream.Close()

	srv := NewServer(testProxyConfig())
	registerProxyWorker(t, srv, "streamer", upstream.URL, true)

	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		rr := httptest.NewRecorder()
		srv.ServeHTTP(rr, proxyRequest(`{"model":"qwen","stream":true,"messages":[]}`))
		done <- rr
	}()

	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("upstream stream did not start")
	}
	waitForActive(t, srv, "streamer", 1)

	close(releaseStream)

	var rr *httptest.ResponseRecorder
	select {
	case rr = <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("proxy did not finish after stream release")
	}
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d: %s", rr.Code, http.StatusOK, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "data: done") {
		t.Fatalf("body = %q, want full stream", rr.Body.String())
	}
	if got := srv.accounting.WorkerActive("streamer"); got != 0 {
		t.Fatalf("accounting active after stream = %d, want 0", got)
	}
	if got := registryActive(srv.workers, "streamer"); got != 0 {
		t.Fatalf("registry active after stream = %d, want 0", got)
	}
}

func TestProxyRouteRequiresClientBearerToken(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"choices":[]}`))
	}))
	defer upstream.Close()

	srv := NewServer(testProxyConfig())
	registerProxyWorker(t, srv, "worker", upstream.URL, true)

	missingAuth := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewBufferString(`{"model":"qwen"}`))
	srv.ServeHTTP(missingAuth, req)
	if missingAuth.Code != http.StatusUnauthorized {
		t.Fatalf("missing auth status = %d, want %d", missingAuth.Code, http.StatusUnauthorized)
	}

	withAuth := httptest.NewRecorder()
	srv.ServeHTTP(withAuth, proxyRequest(`{"model":"qwen"}`))
	if withAuth.Code != http.StatusOK {
		t.Fatalf("authorized status = %d, want %d: %s", withAuth.Code, http.StatusOK, withAuth.Body.String())
	}
}

func TestGatewaySmokeProxiesChatCompletionToLlamaSwap(t *testing.T) {
	fake := testutil.NewFakeLlamaSwap()
	defer fake.Close()
	fake.ExpectedChatAuthorization = "Bearer llama-secret"
	fake.ExpectedChatModel = "qwen"
	fake.ExpectedChatMessages = []map[string]string{
		{"role": "user", "content": "hi"},
	}

	srv := NewServer(testProxyConfig())
	registerProxyWorker(t, srv, "gpu-01", fake.URL(), true)

	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, proxyRequest(`{"model":"qwen","messages":[{"role":"user","content":"hi"}]}`))

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d: %s", rr.Code, http.StatusOK, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "chatcmpl-test") {
		t.Fatalf("body = %q, want fake chat completion id", rr.Body.String())
	}
}

func TestFakeLlamaSwapChatCompletionsRejectsUnexpectedForwardedRequest(t *testing.T) {
	fake := testutil.NewFakeLlamaSwap()
	defer fake.Close()
	fake.ExpectedChatAuthorization = "Bearer llama-secret"
	fake.ExpectedChatModel = "qwen"
	fake.ExpectedChatMessages = []map[string]string{
		{"role": "user", "content": "hi"},
	}

	for _, tc := range []struct {
		name string
		auth string
		body string
	}{
		{
			name: "wrong auth",
			auth: "Bearer wrong",
			body: `{"model":"qwen","messages":[]}`,
		},
		{
			name: "wrong model",
			auth: "Bearer llama-secret",
			body: `{"model":"wrong","messages":[]}`,
		},
		{
			name: "missing messages",
			auth: "Bearer llama-secret",
			body: `{"model":"qwen"}`,
		},
		{
			name: "wrong messages",
			auth: "Bearer llama-secret",
			body: `{"model":"qwen","messages":[{"role":"user","content":"bye"}]}`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req, err := http.NewRequest(http.MethodPost, fake.URL()+"/v1/chat/completions", strings.NewReader(tc.body))
			if err != nil {
				t.Fatalf("new request: %v", err)
			}
			req.Header.Set("Authorization", tc.auth)
			req.Header.Set("Content-Type", "application/json")

			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatalf("post fake chat completions: %v", err)
			}
			defer resp.Body.Close()

			if resp.StatusCode < 400 {
				t.Fatalf("status = %d, want non-2xx rejection", resp.StatusCode)
			}
		})
	}
}

func testProxyConfig() config.GatewayConfig {
	cfg := testGatewayConfig()
	cfg.Tokens.Client = "client-secret"
	cfg.Tokens.LlamaSwap = "llama-secret"
	return cfg
}

func proxyRequest(body string) *http.Request {
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewBufferString(body))
	req.Header.Set("Authorization", "Bearer client-secret")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Request-ID", "req-test")
	return req
}

func registerProxyWorker(t *testing.T, srv *Server, id, baseURL string, running bool) {
	t.Helper()
	hb := protocol.HeartbeatRequest{
		AgentID:      id,
		Tags:         []string{"gpu-4090"},
		LlamaSwapURL: baseURL,
		Artifacts:    map[string]string{"qwen": "ready"},
	}
	if running {
		hb.RunningModels = []protocol.RunningModel{{Model: "qwen", State: "ready"}}
	}
	resp := srv.workers.UpsertHeartbeat(hb, time.Now())
	if resp.WorkerState != string(WorkerActive) {
		t.Fatalf("worker state = %q, want active", resp.WorkerState)
	}
}

func assertOpenAIErrorCode(t *testing.T, body []byte, code string) {
	t.Helper()
	var resp struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.NewDecoder(bytes.NewReader(body)).Decode(&resp); err != nil {
		t.Fatalf("decode OpenAI error: %v; body=%s", err, string(body))
	}
	if resp.Error.Code != code {
		t.Fatalf("error code = %q, want %q; body=%s", resp.Error.Code, code, string(body))
	}
}

func assertNotOpenAIErrorCode(t *testing.T, body []byte, code string) {
	t.Helper()
	var resp struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.NewDecoder(bytes.NewReader(body)).Decode(&resp); err != nil {
		t.Fatalf("decode OpenAI error: %v; body=%s", err, string(body))
	}
	if resp.Error.Code == code {
		t.Fatalf("error code = %q, did not want it; body=%s", resp.Error.Code, string(body))
	}
}

func waitForActive(t *testing.T, srv *Server, workerID string, want int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if srv.accounting.WorkerActive(workerID) == want && registryActive(srv.workers, workerID) == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("active counts for %s: accounting=%d registry=%d, want %d", workerID, srv.accounting.WorkerActive(workerID), registryActive(srv.workers, workerID), want)
}

func registryActive(reg *WorkerRegistry, workerID string) int {
	reg.mu.RLock()
	defer reg.mu.RUnlock()
	return reg.active[workerID]
}
