package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"llm-swap/internal/config"
	"llm-swap/internal/protocol"
)

var defaultProxyHTTPClient = http.DefaultClient
var requestIDSequence uint64

func (s *Server) handleModelProxy(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	requestID := ensureRequestID(r)
	w.Header().Set("X-Request-Id", requestID)

	body, err := io.ReadAll(r.Body)
	if err != nil {
		protocol.WriteOpenAIError(w, http.StatusBadRequest, "invalid_request", "failed to read request body")
		return
	}

	model := ExtractModel(body)
	if model == "" {
		protocol.WriteOpenAIError(w, http.StatusBadRequest, "missing_model", "request body must include a non-empty model")
		return
	}
	if _, ok := s.config.Models[model]; !ok {
		protocol.WriteOpenAIError(w, http.StatusNotFound, "model_not_available", "model is not available")
		return
	}
	modelCfg := s.config.Models[model]
	limitCtx, cancelLimit := queueContext(r.Context(), modelCfg.QueueTimeoutMS)
	defer cancelLimit()
	modelLimitRelease, err := s.acquireLimit(limitCtx, "model:"+model, modelCfg.MaxConcurrency, modelCfg.MaxQueue)
	if err != nil {
		s.observeQueueError(model, err)
		writeQueueError(w, err)
		return
	}
	defer modelLimitRelease()

	exclude := make(map[string]bool)
	var lastDispatchFailure *proxyDispatchFailure
	var lastQueueErr error
	for dispatchAttempts := 0; dispatchAttempts < s.proxyAttempts; {
		if err := r.Context().Err(); err != nil {
			return
		}

		worker, err := (Scheduler{Config: s.config, Workers: s.workers}).Pick(model, time.Now(), exclude)
		if err != nil {
			break
		}
		exclude[worker.ID] = true

		tag := selectedWorkerTag(s.config, worker, model)
		policy, _ := tagPolicy(s.config, tag)
		tagLimitRelease, err := s.acquireLimit(limitCtx, "tag:"+tag, policy.MaxConcurrency, policy.MaxQueue)
		if err != nil {
			if errors.Is(err, ErrQueueFull) {
				lastQueueErr = err
				continue
			}
			s.observeQueueError(model, err)
			writeQueueError(w, err)
			return
		}
		workerLimitRelease, err := s.acquireLimit(limitCtx, "worker:"+worker.ID, policy.WorkerDefaults.MaxConcurrency, policy.WorkerDefaults.MaxQueue)
		if err != nil {
			tagLimitRelease()
			if errors.Is(err, ErrQueueFull) {
				lastQueueErr = err
				continue
			}
			s.observeQueueError(model, err)
			writeQueueError(w, err)
			return
		}

		workerRelease, ok := s.workers.Acquire(worker.ID, time.Now())
		if !ok {
			workerLimitRelease()
			tagLimitRelease()
			continue
		}
		s.logEvent("scheduler_decision", map[string]any{
			"request_id": requestID,
			"model":      model,
			"worker_id":  worker.ID,
			"tag":        tag,
		})
		accountingRelease := s.accounting.Acquire(requestID, model, tag, worker.ID)
		metricsRelease := s.metrics.AcquireActiveRequest(worker.ID, model)
		release := releaseOnce(workerRelease, accountingRelease, metricsRelease, workerLimitRelease, tagLimitRelease)

		dispatchAttempts++
		retry, dispatchFailure, err, statusCode := s.proxyAttempt(w, r, body, model, worker)
		release()
		if dispatchFailure != nil {
			lastDispatchFailure = dispatchFailure
		}
		if err != nil && r.Context().Err() != nil {
			return
		}
		if retry {
			if dispatchFailure != nil {
				s.metrics.ObserveDispatchFailure(model, worker.ID, dispatchFailure.code)
			}
			continue
		}
		if err != nil {
			protocol.WriteOpenAIError(w, http.StatusBadGateway, "upstream_error", "upstream request failed")
			statusCode = http.StatusBadGateway
		}
		s.metrics.ObserveRequest(model, worker.ID, statusCode, time.Since(start))
		s.logEvent("request", map[string]any{
			"request_id":  requestID,
			"model":       model,
			"worker_id":   worker.ID,
			"tag":         tag,
			"status_code": statusCode,
			"latency_ms":  time.Since(start).Milliseconds(),
		})
		return
	}

	if r.Context().Err() != nil {
		return
	}
	if lastDispatchFailure != nil {
		s.metrics.ObserveDispatchFailure(model, "", lastDispatchFailure.code)
		lastDispatchFailure.write(w)
		return
	}
	if lastQueueErr != nil {
		s.observeQueueError(model, lastQueueErr)
		writeQueueError(w, lastQueueErr)
		return
	}
	protocol.WriteOpenAIError(w, http.StatusServiceUnavailable, "no_healthy_worker", "no healthy worker is available for the requested model")
}

func queueContext(parent context.Context, timeoutMS int) (context.Context, context.CancelFunc) {
	if timeoutMS <= 0 {
		return parent, func() {}
	}
	return context.WithTimeout(parent, time.Duration(timeoutMS)*time.Millisecond)
}

func (s *Server) acquireLimit(ctx context.Context, key string, maxActive, maxQueue int) (func(), error) {
	if s.limiter == nil {
		return func() {}, nil
	}
	return s.limiter.Acquire(ctx, key, maxActive, maxQueue)
}

func writeQueueError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrQueueTimeout):
		protocol.WriteOpenAIError(w, http.StatusTooManyRequests, "queue_timeout", "request waited longer than the configured queue timeout")
	default:
		protocol.WriteOpenAIError(w, http.StatusTooManyRequests, "queue_full", "request queue is full")
	}
}

func ensureRequestID(r *http.Request) string {
	requestID := strings.TrimSpace(r.Header.Get("X-Request-Id"))
	if requestID == "" {
		requestID = nextRequestID()
		r.Header.Set("X-Request-Id", requestID)
	}
	return requestID
}

func requestIDFromHeader(r *http.Request) string {
	return strings.TrimSpace(r.Header.Get("X-Request-Id"))
}

func nextRequestID() string {
	seq := atomic.AddUint64(&requestIDSequence, 1)
	return fmt.Sprintf("req-%d-%d", time.Now().UnixNano(), seq)
}

func (s *Server) observeQueueError(model string, err error) {
	if s.metrics == nil {
		return
	}
	if errors.Is(err, ErrQueueTimeout) {
		s.metrics.ObserveQueueEvent(model, "queue_timeout")
		return
	}
	s.metrics.ObserveQueueEvent(model, "queue_full")
}

func (s *Server) logEvent(event string, fields map[string]any) {
	if s.logger == nil {
		return
	}
	if fields == nil {
		fields = make(map[string]any)
	}
	fields["event"] = event
	data, err := json.Marshal(fields)
	if err != nil {
		s.logger.Printf(`{"event":"%s","log_error":%q}`, event, err.Error())
		return
	}
	s.logger.Println(string(data))
}

type proxyDispatchFailure struct {
	status  int
	code    string
	message string
}

func (f proxyDispatchFailure) write(w http.ResponseWriter) {
	protocol.WriteOpenAIError(w, f.status, f.code, f.message)
}

func workerUnavailableFailure() *proxyDispatchFailure {
	return &proxyDispatchFailure{
		status:  http.StatusServiceUnavailable,
		code:    "worker_unavailable",
		message: "selected worker is unavailable",
	}
}

func upstreamRetryExhaustedFailure(status int) *proxyDispatchFailure {
	message := "upstream returned a retryable status after exhausting proxy attempts"
	if text := http.StatusText(status); text != "" {
		message = "upstream returned " + text + " after exhausting proxy attempts"
	}
	return &proxyDispatchFailure{
		status:  status,
		code:    "upstream_retry_exhausted",
		message: message,
	}
}

func (s *Server) proxyAttempt(w http.ResponseWriter, r *http.Request, body []byte, model string, worker Worker) (bool, *proxyDispatchFailure, error, int) {
	upstreamURL, err := upstreamRequestURL(worker.LlamaSwapURL, r.URL)
	if err != nil {
		return true, workerUnavailableFailure(), err, 0
	}

	req, err := http.NewRequestWithContext(r.Context(), r.Method, upstreamURL, bytes.NewReader(body))
	if err != nil {
		return true, workerUnavailableFailure(), err, 0
	}
	copyRequestHeaders(req.Header, r.Header)
	req.Header.Set("X-Request-Id", requestIDFromHeader(r))
	req.Header.Set("X-Gateway-Model", model)
	req.Header.Set("X-Gateway-Worker", worker.ID)
	if s.config.Tokens.LlamaSwap != "" {
		req.Header.Set("Authorization", "Bearer "+s.config.Tokens.LlamaSwap)
	} else {
		req.Header.Del("Authorization")
	}
	req.ContentLength = int64(len(body))

	resp, err := defaultProxyHTTPClient.Do(req)
	if err != nil {
		if retryableProxyError(err, r.Context()) {
			return true, workerUnavailableFailure(), err, 0
		}
		return false, nil, err, 0
	}
	defer resp.Body.Close()

	if retryableUpstreamStatus(resp.StatusCode) {
		_, _ = io.Copy(io.Discard, resp.Body)
		return true, upstreamRetryExhaustedFailure(resp.StatusCode), nil, resp.StatusCode
	}
	if resp.StatusCode == http.StatusNotFound {
		respBody, err := io.ReadAll(resp.Body)
		if err != nil {
			return false, nil, err, resp.StatusCode
		}
		if retryablePlatform404(resp.Header, respBody) {
			return true, upstreamRetryExhaustedFailure(resp.StatusCode), nil, resp.StatusCode
		}

		copyResponseHeaders(w.Header(), resp.Header)
		w.WriteHeader(resp.StatusCode)
		_, _ = w.Write(respBody)
		return false, nil, nil, resp.StatusCode
	}

	copyResponseHeaders(w.Header(), resp.Header)
	w.WriteHeader(resp.StatusCode)
	if err := copyResponseBody(w, resp.Body); err != nil {
		return false, nil, nil, resp.StatusCode
	}
	return false, nil, nil, resp.StatusCode
}

func upstreamRequestURL(baseURL string, requestURL *url.URL) (string, error) {
	base, err := url.Parse(strings.TrimRight(baseURL, "/"))
	if err != nil {
		return "", err
	}
	if base.Scheme == "" || base.Host == "" {
		return "", &url.Error{Op: "parse", URL: baseURL, Err: errMissingBaseURL}
	}

	base.Path = singleJoiningSlash(base.Path, requestURL.Path)
	base.RawQuery = requestURL.RawQuery
	base.Fragment = ""
	return base.String(), nil
}

type proxyURLError string

func (e proxyURLError) Error() string {
	return string(e)
}

const errMissingBaseURL proxyURLError = "missing upstream base url"

func copyRequestHeaders(dst, src http.Header) {
	connectionHeaders := connectionHeaderNames(src)
	for key, values := range src {
		if hopByHopHeader(key) || connectionHeaders[strings.ToLower(key)] {
			continue
		}
		for _, value := range values {
			dst.Add(key, value)
		}
	}
}

func copyResponseHeaders(dst, src http.Header) {
	connectionHeaders := connectionHeaderNames(src)
	for key, values := range src {
		if hopByHopHeader(key) || connectionHeaders[strings.ToLower(key)] {
			continue
		}
		for _, value := range values {
			dst.Add(key, value)
		}
	}
}

func connectionHeaderNames(h http.Header) map[string]bool {
	names := make(map[string]bool)
	for _, value := range h.Values("Connection") {
		for _, name := range strings.Split(value, ",") {
			name = strings.ToLower(strings.TrimSpace(name))
			if name != "" {
				names[name] = true
			}
		}
	}
	return names
}

func hopByHopHeader(name string) bool {
	switch strings.ToLower(name) {
	case "connection", "keep-alive", "proxy-authenticate", "proxy-authorization", "te", "trailer", "transfer-encoding", "upgrade":
		return true
	default:
		return false
	}
}

func singleJoiningSlash(a, b string) string {
	aslash := strings.HasSuffix(a, "/")
	bslash := strings.HasPrefix(b, "/")
	switch {
	case aslash && bslash:
		return a + b[1:]
	case !aslash && !bslash:
		return a + "/" + b
	default:
		return a + b
	}
}

func retryableUpstreamStatus(status int) bool {
	switch status {
	case http.StatusTooManyRequests, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return true
	default:
		return false
	}
}

func retryablePlatform404(header http.Header, body []byte) bool {
	contentType := strings.ToLower(strings.TrimSpace(header.Get("Content-Type")))
	mediaType, _, _ := strings.Cut(contentType, ";")
	if strings.TrimSpace(mediaType) == "text/html" {
		return true
	}

	trimmed := bytes.TrimSpace(body)
	lowerBody := bytes.ToLower(trimmed)
	return bytes.HasPrefix(lowerBody, []byte("<!doctype html>")) || bytes.HasPrefix(lowerBody, []byte("<html"))
}

func retryableProxyError(err error, ctx context.Context) bool {
	if ctx.Err() != nil {
		return false
	}
	return err != nil
}

func copyResponseBody(w http.ResponseWriter, body io.Reader) error {
	flusher, _ := w.(http.Flusher)
	buf := make([]byte, 32*1024)
	for {
		n, readErr := body.Read(buf)
		if n > 0 {
			if _, err := w.Write(buf[:n]); err != nil {
				return err
			}
			if flusher != nil {
				flusher.Flush()
			}
		}
		if readErr != nil {
			if readErr == io.EOF {
				return nil
			}
			return readErr
		}
	}
}

func releaseOnce(releases ...func()) func() {
	var once sync.Once
	return func() {
		once.Do(func() {
			for i := len(releases) - 1; i >= 0; i-- {
				if releases[i] != nil {
					releases[i]()
				}
			}
		})
	}
}

func selectedWorkerTag(cfg config.GatewayConfig, worker Worker, model string) string {
	for _, tag := range worker.Tags {
		policy, ok := tagPolicy(cfg, tag)
		if !ok {
			continue
		}
		if allowedModel(policy, model) {
			return tag
		}
	}
	if len(worker.Tags) > 0 {
		return worker.Tags[0]
	}
	return ""
}
