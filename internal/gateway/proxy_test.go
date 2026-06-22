package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
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

func TestLlamaSwapClientUnloadAllPostsWithBearerToken(t *testing.T) {
	var gotPath string
	var gotAuth string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusNoContent)
	}))
	defer upstream.Close()

	client := LlamaSwapClient{BearerToken: "llama-secret"}

	if err := client.UnloadAll(context.Background(), upstream.URL); err != nil {
		t.Fatalf("UnloadAll returned error: %v", err)
	}
	if gotPath != "/api/models/unload" {
		t.Fatalf("path = %q, want unload-all path", gotPath)
	}
	if gotAuth != "Bearer llama-secret" {
		t.Fatalf("authorization = %q, want bearer token", gotAuth)
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

func TestProxyGeneratesRequestIDForwardsGatewayHeadersAndLogs(t *testing.T) {
	var gotRequestID string
	var gotGatewayModel string
	var gotGatewayWorker string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotRequestID = r.Header.Get("X-Request-Id")
		gotGatewayModel = r.Header.Get("X-Gateway-Model")
		gotGatewayWorker = r.Header.Get("X-Gateway-Worker")
		writeJSON(w, map[string]any{"ok": true})
	}))
	defer upstream.Close()

	srv := NewServer(testProxyConfig())
	var logs bytes.Buffer
	srv.logger = log.New(&logs, "", 0)
	registerProxyWorker(t, srv, "worker-a", upstream.URL, true)

	req := proxyRequest(`{"model":"qwen","messages":[]}`)
	req.Header.Del("X-Request-ID")
	rr := httptest.NewRecorder()

	srv.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d: %s", rr.Code, http.StatusOK, rr.Body.String())
	}
	if gotRequestID == "" {
		t.Fatal("upstream X-Request-Id is empty")
	}
	if rr.Header().Get("X-Request-Id") != gotRequestID {
		t.Fatalf("response X-Request-Id = %q, want forwarded %q", rr.Header().Get("X-Request-Id"), gotRequestID)
	}
	if gotGatewayModel != "qwen" {
		t.Fatalf("X-Gateway-Model = %q, want qwen", gotGatewayModel)
	}
	if gotGatewayWorker != "worker-a" {
		t.Fatalf("X-Gateway-Worker = %q, want worker-a", gotGatewayWorker)
	}
	logText := logs.String()
	for _, want := range []string{`"event":"request"`, `"event":"scheduler_decision"`, `"request_id":"` + gotRequestID + `"`, `"model":"qwen"`, `"worker_id":"worker-a"`} {
		if !strings.Contains(logText, want) {
			t.Fatalf("logs missing %s:\n%s", want, logText)
		}
	}
}

func TestProxyNormalizesTopKZeroForSGLangModels(t *testing.T) {
	var gotTopK float64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode upstream body: %v", err)
		}
		gotTopK, _ = body["top_k"].(float64)
		writeJSON(w, map[string]any{"ok": true})
	}))
	defer upstream.Close()

	cfg := testProxyConfig()
	model := cfg.Models["qwen"]
	model.Run = "PORT=${PORT} /opt/llmswap/bin/sglang.server {{model_path}} --sampling-defaults openai"
	cfg.Models["qwen"] = model
	srv := NewServer(cfg)
	registerProxyWorker(t, srv, "worker-a", upstream.URL, true)

	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, proxyRequest(`{"model":"qwen","messages":[],"top_k":0}`))

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d: %s", rr.Code, http.StatusOK, rr.Body.String())
	}
	if gotTopK != -1 {
		t.Fatalf("upstream top_k = %v, want -1", gotTopK)
	}
}

func TestProxyNormalizesTransformersImagePartsForSGLangModels(t *testing.T) {
	var gotPart map[string]any
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Messages []struct {
				Content []map[string]any `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode upstream body: %v", err)
		}
		if len(body.Messages) != 1 || len(body.Messages[0].Content) != 2 {
			t.Fatalf("unexpected upstream messages: %+v", body.Messages)
		}
		gotPart = body.Messages[0].Content[0]
		writeJSON(w, map[string]any{"ok": true})
	}))
	defer upstream.Close()

	cfg := testProxyConfig()
	model := cfg.Models["qwen"]
	model.Run = "PORT=${PORT} /opt/llmswap/bin/sglang.server {{model_path}} --sampling-defaults openai"
	cfg.Models["qwen"] = model
	srv := NewServer(cfg)
	registerProxyWorker(t, srv, "worker-a", upstream.URL, true)

	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, proxyRequest(`{"model":"qwen","messages":[{"role":"user","content":[{"type":"image","url":"https://example.com/dog.png"},{"type":"text","text":"图片中是什么"}]}]}`))

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d: %s", rr.Code, http.StatusOK, rr.Body.String())
	}
	if gotPart["type"] != "image_url" {
		t.Fatalf("upstream image type = %v, want image_url; part=%+v", gotPart["type"], gotPart)
	}
	imageURL, ok := gotPart["image_url"].(map[string]any)
	if !ok {
		t.Fatalf("upstream image_url = %T, want object; part=%+v", gotPart["image_url"], gotPart)
	}
	if imageURL["url"] != "https://example.com/dog.png" {
		t.Fatalf("upstream image url = %v, want source url", imageURL["url"])
	}
	if _, ok := gotPart["url"]; ok {
		t.Fatalf("upstream image part still has url field: %+v", gotPart)
	}
}

func TestProxyWritesRequestLogAndAggregatesUsage(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{
			"id":     "chatcmpl-test",
			"object": "chat.completion",
			"model":  "qwen",
			"choices": []map[string]any{{
				"finish_reason": "stop",
				"message":       map[string]any{"role": "assistant", "content": "hello"},
			}},
			"usage": map[string]any{
				"prompt_tokens":     7,
				"completion_tokens": 3,
				"total_tokens":      10,
				"reasoning_tokens":  2,
				"prompt_tokens_details": map[string]any{
					"cached_tokens": 4,
				},
			},
		})
	}))
	defer upstream.Close()

	logPath := filepath.Join(t.TempDir(), "gateway-requests.jsonl")
	srv := NewServerWithGatewayPersistence(testProxyConfig(), logPath)
	registerProxyWorker(t, srv, "worker-a", upstream.URL, true)

	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, proxyRequest(`{"model":"qwen","messages":[{"role":"user","content":[{"type":"text","text":"hi"},{"type":"image_url","image_url":{"url":"https://example.com/a.png"}}]}],"max_tokens":32,"temperature":0.2,"top_p":0.9}`))

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d: %s", rr.Code, http.StatusOK, rr.Body.String())
	}
	entry := readSingleRequestLogEntry(t, logPath)
	if entry.Model != "qwen" || entry.WorkerID != "worker-a" || entry.StatusCode != http.StatusOK {
		t.Fatalf("unexpected log entry: %+v", entry)
	}
	if entry.PromptTokens != 7 || entry.CompletionTokens != 3 || entry.TotalTokens != 10 || entry.CacheTokens != 4 || entry.ReasoningTokens != 2 {
		t.Fatalf("log tokens = prompt:%d completion:%d total:%d cache:%d reasoning:%d", entry.PromptTokens, entry.CompletionTokens, entry.TotalTokens, entry.CacheTokens, entry.ReasoningTokens)
	}
	if entry.MessageCount != 1 || entry.ImageCount != 1 || entry.MaxTokens != 32 || entry.FinishReason != "stop" {
		t.Fatalf("log request metadata = %+v", entry)
	}
	if got := srv.access.ModelTotalTokens("qwen"); got != 10 {
		t.Fatalf("persisted total tokens = %d, want 10", got)
	}
}

func TestProxyWritesStreamingRequestLogUsage(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\n"))
		_, _ = w.Write([]byte("data: {\"choices\":[{\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":5,\"completion_tokens\":2,\"total_tokens\":7}}\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer upstream.Close()

	logPath := filepath.Join(t.TempDir(), "gateway-requests.jsonl")
	srv := NewServerWithGatewayPersistence(testProxyConfig(), logPath)
	registerProxyWorker(t, srv, "worker-a", upstream.URL, true)

	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, proxyRequest(`{"model":"qwen","messages":[],"stream":true,"stream_options":{"include_usage":true}}`))

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d: %s", rr.Code, http.StatusOK, rr.Body.String())
	}
	entry := readSingleRequestLogEntry(t, logPath)
	if !entry.Stream {
		t.Fatalf("stream = false, want true: %+v", entry)
	}
	if entry.PromptTokens != 5 || entry.CompletionTokens != 2 || entry.TotalTokens != 7 || entry.FinishReason != "stop" {
		t.Fatalf("stream log entry = %+v", entry)
	}
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

func TestProxyHonorsConfiguredProxyAttempts(t *testing.T) {
	var firstRequests atomic.Int32
	var secondRequests atomic.Int32
	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		firstRequests.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer first.Close()
	second := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		secondRequests.Add(1)
		_, _ = w.Write([]byte(`{"id":"chatcmpl-second","choices":[]}`))
	}))
	defer second.Close()

	cfg := testProxyConfig()
	cfg.Gateway.ProxyAttempts = 1
	srv := NewServer(cfg)
	registerProxyWorker(t, srv, "first", first.URL, true)
	registerProxyWorker(t, srv, "second", second.URL, false)

	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, proxyRequest(`{"model":"qwen","messages":[]}`))

	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d: %s", rr.Code, http.StatusServiceUnavailable, rr.Body.String())
	}
	assertOpenAIErrorCode(t, rr.Body.Bytes(), "upstream_retry_exhausted")
	if firstRequests.Load() != 1 {
		t.Fatalf("first requests = %d, want 1", firstRequests.Load())
	}
	if secondRequests.Load() != 0 {
		t.Fatalf("second requests = %d, want 0 when proxy_attempts=1", secondRequests.Load())
	}
}

func TestProxyRetriesHTML404FromWorkerPlatform(t *testing.T) {
	var firstRequests atomic.Int32
	var secondRequests atomic.Int32
	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		firstRequests.Add(1)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte("<!DOCTYPE html><html><body>404 Not Found</body></html>"))
	}))
	defer first.Close()
	second := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		secondRequests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl-second","choices":[]}`))
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
	if !strings.Contains(rr.Body.String(), "chatcmpl-second") {
		t.Fatalf("body = %q, want second worker response", rr.Body.String())
	}
	if strings.Contains(rr.Body.String(), "404 Not Found") {
		t.Fatalf("body = %q, did not want platform 404 body", rr.Body.String())
	}
	if firstRequests.Load() != 1 {
		t.Fatalf("first requests = %d, want 1", firstRequests.Load())
	}
	if secondRequests.Load() != 1 {
		t.Fatalf("second requests = %d, want 1", secondRequests.Load())
	}
}

func TestProxyForwardsJSON404WithoutRetry(t *testing.T) {
	var firstRequests atomic.Int32
	var secondRequests atomic.Int32
	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		firstRequests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":{"code":"model_not_found","message":"missing"}}`))
	}))
	defer first.Close()
	second := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		secondRequests.Add(1)
		_, _ = w.Write([]byte(`{"id":"chatcmpl-second","choices":[]}`))
	}))
	defer second.Close()

	srv := NewServer(testProxyConfig())
	registerProxyWorker(t, srv, "first", first.URL, true)
	registerProxyWorker(t, srv, "second", second.URL, false)

	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, proxyRequest(`{"model":"qwen","messages":[]}`))

	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusNotFound)
	}
	if rr.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("Content-Type = %q, want application/json", rr.Header().Get("Content-Type"))
	}
	if !strings.Contains(rr.Body.String(), "model_not_found") {
		t.Fatalf("body = %q, want upstream JSON 404", rr.Body.String())
	}
	if firstRequests.Load() != 1 {
		t.Fatalf("first requests = %d, want 1", firstRequests.Load())
	}
	if secondRequests.Load() != 0 {
		t.Fatalf("second requests = %d, want 0", secondRequests.Load())
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

func TestProxyReturnsQueueFullWhenModelLimitIsFull(t *testing.T) {
	started := make(chan struct{})
	releaseUpstream := make(chan struct{})
	var releaseOnce sync.Once
	release := func() {
		releaseOnce.Do(func() { close(releaseUpstream) })
	}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-releaseUpstream
		_, _ = w.Write([]byte(`{"choices":[]}`))
	}))
	defer func() {
		release()
		upstream.Close()
	}()

	cfg := testProxyConfig()
	model := cfg.Models["qwen"]
	model.MaxConcurrency = 1
	model.MaxQueue = 0
	cfg.Models["qwen"] = model
	srv := NewServer(cfg)
	registerProxyWorker(t, srv, "worker", upstream.URL, true)

	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		rr := httptest.NewRecorder()
		srv.ServeHTTP(rr, proxyRequest(`{"model":"qwen","messages":[]}`))
		done <- rr
	}()

	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("first upstream request did not start")
	}

	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, proxyRequest(`{"model":"qwen","messages":[]}`))
	if rr.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want %d: %s", rr.Code, http.StatusTooManyRequests, rr.Body.String())
	}
	assertOpenAIErrorCode(t, rr.Body.Bytes(), "queue_full")

	release()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("first proxy request did not finish")
	}
}

func TestProxyWorkerLimitComesFromGatewayTagPolicy(t *testing.T) {
	started := make(chan struct{})
	releaseUpstream := make(chan struct{})
	var releaseOnce sync.Once
	release := func() {
		releaseOnce.Do(func() { close(releaseUpstream) })
	}
	var upstreamRequests atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if upstreamRequests.Add(1) > 1 {
			_, _ = w.Write([]byte(`{"choices":[]}`))
			return
		}
		close(started)
		<-releaseUpstream
		_, _ = w.Write([]byte(`{"choices":[]}`))
	}))
	defer func() {
		release()
		upstream.Close()
	}()

	cfg := testProxyConfig()
	policy := cfg.TagPolicies["gpu-4090"]
	policy.WorkerDefaults = config.WorkerDefaults{MaxConcurrency: 1, MaxQueue: 0}
	cfg.TagPolicies["gpu-4090"] = policy
	srv := NewServer(cfg)
	registerProxyWorker(t, srv, "worker", upstream.URL, true)

	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		rr := httptest.NewRecorder()
		srv.ServeHTTP(rr, proxyRequest(`{"model":"qwen","messages":[]}`))
		done <- rr
	}()

	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("first upstream request did not start")
	}

	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, proxyRequest(`{"model":"qwen","messages":[]}`))
	if rr.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want %d: %s", rr.Code, http.StatusTooManyRequests, rr.Body.String())
	}
	assertOpenAIErrorCode(t, rr.Body.Bytes(), "queue_full")

	release()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("first proxy request did not finish")
	}
}

func TestProxySkipsFullWorkerQueueWhenAnotherWorkerAvailable(t *testing.T) {
	holdStarted := make(chan struct{})
	releaseHeld := make(chan struct{})
	var releaseOnce sync.Once
	release := func() {
		releaseOnce.Do(func() { close(releaseHeld) })
	}
	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(holdStarted)
		<-releaseHeld
		_, _ = w.Write([]byte(`{"choices":[]}`))
	}))
	defer func() {
		release()
		first.Close()
	}()

	var secondRequests atomic.Int32
	second := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		secondRequests.Add(1)
		_, _ = w.Write([]byte(`{"id":"chatcmpl-second","choices":[]}`))
	}))
	defer second.Close()

	cfg := testProxyConfig()
	policy := cfg.TagPolicies["gpu-4090"]
	policy.WorkerDefaults = config.WorkerDefaults{MaxConcurrency: 1, MaxQueue: 0}
	cfg.TagPolicies["gpu-4090"] = policy
	srv := NewServer(cfg)
	registerProxyWorker(t, srv, "gpu-a", first.URL, true)
	registerProxyWorker(t, srv, "gpu-b", second.URL, false)

	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		rr := httptest.NewRecorder()
		srv.ServeHTTP(rr, proxyRequest(`{"model":"qwen","messages":[]}`))
		done <- rr
	}()

	select {
	case <-holdStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("first upstream request did not start")
	}

	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, proxyRequest(`{"model":"qwen","messages":[]}`))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d: %s", rr.Code, http.StatusOK, rr.Body.String())
	}
	if secondRequests.Load() != 1 {
		t.Fatalf("second requests = %d, want 1", secondRequests.Load())
	}

	release()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("first proxy request did not finish")
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

func readSingleRequestLogEntry(t *testing.T, path string) RequestLogEntry {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read request log: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 1 {
		t.Fatalf("request log lines = %d, want 1:\n%s", len(lines), string(data))
	}
	var entry RequestLogEntry
	if err := json.Unmarshal([]byte(lines[0]), &entry); err != nil {
		t.Fatalf("decode request log entry: %v; line=%s", err, lines[0])
	}
	return entry
}
