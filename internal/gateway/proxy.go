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
	cfg := s.currentConfig()
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
	if _, ok := cfg.Models[model]; !ok {
		protocol.WriteOpenAIError(w, http.StatusNotFound, "model_not_available", "model is not available")
		return
	}
	modelCfg := cfg.Models[model]
	body = normalizeProxyRequestBody(body, modelCfg)
	baseLogEntry := requestLogEntryFromBody(requestID, model, body)
	limitCtx, cancelLimit := queueContext(r.Context(), modelCfg.QueueTimeoutMS)
	defer cancelLimit()
	modelLimitRelease, _, err := s.acquireObservedLimit(limitCtx, requestID, model, "model", "model:"+model, modelCfg.MaxConcurrency, modelCfg.MaxQueue, s.replicaStats(model, time.Now()))
	if err != nil {
		writeQueueError(w, err)
		return
	}
	defer modelLimitRelease()

	exclude := make(map[string]bool)
	var lastDispatchFailure *proxyDispatchFailure
	var lastQueueErr error
	var lastScheduleDecision ScheduleDecision
	var lastScheduleErr error
	for dispatchAttempts := 0; dispatchAttempts < s.proxyAttempts; {
		if err := r.Context().Err(); err != nil {
			return
		}

		now := time.Now()
		decision, err := (Scheduler{
			Config:    cfg,
			Workers:   s.workers,
			Access:    s.access,
			Cooldowns: s.replicaCooldowns.Snapshot(now),
		}).PickDecision(model, now, exclude)
		if err != nil {
			lastScheduleDecision = decision
			lastScheduleErr = err
			break
		}
		worker := decision.Worker
		exclude[worker.ID] = true

		tag := selectedWorkerTag(cfg, worker, model)
		policy, _ := tagPolicy(cfg, tag)
		tagLimitRelease, _, err := s.acquireObservedLimit(limitCtx, requestID, model, "tag", "tag:"+tag, policy.MaxConcurrency, policy.MaxQueue, replicaStatsFromDecision(decision))
		if err != nil {
			if errors.Is(err, ErrQueueFull) {
				lastQueueErr = err
				continue
			}
			writeQueueError(w, err)
			return
		}
		workerLimitRelease, _, err := s.acquireObservedLimit(limitCtx, requestID, model, "worker", "worker:"+worker.ID, policy.WorkerDefaults.MaxConcurrency, policy.WorkerDefaults.MaxQueue, replicaStatsFromDecision(decision))
		if err != nil {
			tagLimitRelease()
			if errors.Is(err, ErrQueueFull) {
				lastQueueErr = err
				continue
			}
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
			"request_id":        requestID,
			"model":             model,
			"worker_id":         worker.ID,
			"tag":               tag,
			"reason":            decision.Reason,
			"ready_replicas":    decision.ReadyReplicas,
			"occupied_replicas": decision.OccupiedReplicas,
			"max_loaded":        decision.MaxLoaded,
			"max_loaded_auto":   decision.MaxLoadedAuto,
			"candidates":        decision.Candidates,
		})
		accountingRelease := s.accounting.Acquire(requestID, model, tag, worker.ID)
		metricsRelease := s.metrics.AcquireActiveRequest(worker.ID, model)
		release := releaseOnce(workerRelease, accountingRelease, metricsRelease, workerLimitRelease, tagLimitRelease)

		dispatchAttempts++
		retry, dispatchFailure, err, statusCode, responseEntry := s.proxyAttempt(w, r, body, model, worker, cfg.Tokens.LlamaSwap)
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
				s.metrics.ObserveProxyRetry(model, worker.ID, dispatchFailure.code)
				s.logEvent("proxy_retry", map[string]any{
					"request_id":  requestID,
					"model":       model,
					"worker_id":   worker.ID,
					"reason":      dispatchFailure.code,
					"status_code": statusCode,
					"attempt":     dispatchAttempts,
				})
				s.markReplicaCooldown(requestID, model, worker, dispatchFailure, statusCode, dispatchAttempts)
			}
			continue
		}
		if err != nil {
			protocol.WriteOpenAIError(w, http.StatusBadGateway, "upstream_error", "upstream request failed")
			statusCode = http.StatusBadGateway
		}
		entry := mergeRequestLogEntry(baseLogEntry, responseEntry)
		entry.Time = time.Now()
		entry.WorkerID = worker.ID
		entry.Tag = tag
		entry.StatusCode = statusCode
		entry.DurationMS = time.Since(start).Milliseconds()
		entry.RetryCount = dispatchAttempts - 1
		if statusCode > 0 && statusCode < http.StatusInternalServerError {
			s.clearReplicaCooldown(requestID, model, worker)
		}
		s.recordRequestStats(entry)
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
		s.logEvent("proxy_retry_exhausted", map[string]any{
			"request_id": requestID,
			"model":      model,
			"reason":     lastDispatchFailure.code,
		})
		lastDispatchFailure.write(w)
		return
	}
	if lastQueueErr != nil {
		s.observeQueueError(model, lastQueueErr)
		writeQueueError(w, lastQueueErr)
		return
	}
	if lastScheduleErr != nil {
		s.observeNoReady(model, requestID, lastScheduleDecision)
	}
	protocol.WriteOpenAIError(w, http.StatusServiceUnavailable, "no_healthy_worker", "no healthy worker is available for the requested model")
}

func (s *Server) markReplicaCooldown(requestID, model string, worker Worker, failure *proxyDispatchFailure, statusCode int, attempt int) {
	if s == nil || s.replicaCooldowns == nil || failure == nil {
		return
	}
	now := time.Now()
	entry, marked := s.replicaCooldowns.Mark(worker.ID, model, failure.code, now)
	if !marked {
		return
	}
	if s.metrics != nil {
		s.metrics.ObserveReplicaCooldownMark(entry)
	}
	s.logEvent("replica_unhealthy_marked", map[string]any{
		"request_id":       requestID,
		"model":            model,
		"worker_id":        worker.ID,
		"reason":           failure.code,
		"status_code":      statusCode,
		"attempt":          attempt,
		"cooldown_seconds": entry.RemainingSeconds,
		"cooldown_until":   entry.CooldownUntil.UTC(),
		"failure_count":    entry.FailureCount,
	})
}

func (s *Server) clearReplicaCooldown(requestID, model string, worker Worker) {
	if s == nil || s.replicaCooldowns == nil {
		return
	}
	entry, ok := s.replicaCooldowns.Clear(worker.ID, model, time.Now())
	if !ok {
		return
	}
	if s.metrics != nil {
		s.metrics.ObserveReplicaCooldownClear(entry)
	}
	s.logEvent("replica_unhealthy_cleared", map[string]any{
		"request_id": requestID,
		"model":      model,
		"worker_id":  worker.ID,
		"reason":     entry.Reason,
	})
}

func (s *Server) recordRequestStats(entry RequestLogEntry) {
	if s == nil {
		return
	}
	if s.access != nil {
		s.access.RecordRequest(entry)
	}
	s.recordRecentRequest(entry)
	if s.metrics != nil {
		s.metrics.ObserveRequestTokens(entry)
	}
	if s.pressure != nil {
		s.pressure.RecordRequest(PressureRequestObservation{
			Time:        entry.Time,
			Model:       entry.Model,
			WorkerID:    entry.WorkerID,
			TotalTokens: entry.TotalTokens,
			DurationMS:  entry.DurationMS,
			StatusCode:  entry.StatusCode,
		})
	}
	if s.requestLogPath != "" {
		if err := appendRequestLog(s.requestLogPath, entry); err != nil {
			s.logEvent("request_log_write_error", map[string]any{"error": err.Error(), "request_id": entry.RequestID})
		}
	}
}

func (s *Server) recordRecentRequest(entry RequestLogEntry) {
	if s == nil || entry.Model == "" {
		return
	}
	s.requestMu.Lock()
	defer s.requestMu.Unlock()

	s.recentRequests = append(s.recentRequests, entry)
	if len(s.recentRequests) > uiEventLimit {
		s.recentRequests = append([]RequestLogEntry(nil), s.recentRequests[len(s.recentRequests)-uiEventLimit:]...)
	}
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

func (s *Server) acquireObservedLimit(ctx context.Context, requestID, model, keyType, key string, maxActive, maxQueue int, replicas queueReplicaStats) (func(), QueueAcquireStats, error) {
	if s.limiter == nil {
		return func() {}, QueueAcquireStats{Result: QueueResultAdmitted, MaxConcurrency: maxActive, MaxQueue: maxQueue}, nil
	}
	release, stats, err := s.limiter.AcquireWithStats(ctx, key, maxActive, maxQueue)
	if maxActive > 0 || err != nil || stats.Waited {
		s.observeQueue(model, requestID, keyType, key, stats, replicas)
	}
	return release, stats, err
}

type queueReplicaStats struct {
	readyReplicas    int
	occupiedReplicas int
	maxLoaded        int
}

func replicaStatsFromDecision(decision ScheduleDecision) queueReplicaStats {
	return queueReplicaStats{
		readyReplicas:    decision.ReadyReplicas,
		occupiedReplicas: decision.OccupiedReplicas,
		maxLoaded:        decision.MaxLoaded,
	}
}

func (s *Server) replicaStats(model string, now time.Time) queueReplicaStats {
	out := queueReplicaStats{}
	cfg := s.currentConfig()
	modelCfg, ok := cfg.Models[model]
	if ok {
		out.maxLoaded = modelCfg.EffectiveMaxLoaded()
	}
	if s.workers == nil {
		return out
	}
	for _, worker := range s.workers.Snapshot(now) {
		if !s.workers.Healthy(worker.ID, now) {
			continue
		}
		if !workerAllowsModel(cfg, worker, model) || !artifactReady(worker, model) {
			continue
		}
		state, running := runningModelState(worker, model)
		if running {
			out.occupiedReplicas++
		}
		if strings.EqualFold(state, "ready") {
			out.readyReplicas++
		}
	}
	return out
}

func normalizeProxyRequestBody(body []byte, modelCfg config.Model) []byte {
	if !isSGLangModel(modelCfg) {
		return body
	}

	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	var payload map[string]any
	if err := decoder.Decode(&payload); err != nil {
		return body
	}
	changed := normalizeTopK(payload)
	changed = normalizeTransformersMediaParts(payload) || changed

	if !changed {
		return body
	}
	normalized, err := json.Marshal(payload)
	if err != nil {
		return body
	}
	return normalized
}

func normalizeTopK(payload map[string]any) bool {
	rawTopK, ok := payload["top_k"]
	if !ok {
		return false
	}
	if !jsonNumberIsZero(rawTopK) {
		return false
	}
	payload["top_k"] = -1
	return true
}

func jsonNumberIsZero(value any) bool {
	switch typed := value.(type) {
	case json.Number:
		number, err := typed.Float64()
		return err == nil && number == 0
	case float64:
		return typed == 0
	default:
		return false
	}
}

func normalizeTransformersMediaParts(payload map[string]any) bool {
	messages, ok := payload["messages"].([]any)
	if !ok {
		return false
	}

	changed := false
	for _, rawMessage := range messages {
		message, ok := rawMessage.(map[string]any)
		if !ok {
			continue
		}
		content, ok := message["content"].([]any)
		if !ok {
			continue
		}
		for _, rawPart := range content {
			part, ok := rawPart.(map[string]any)
			if !ok {
				continue
			}
			if normalizeTransformersMediaPart(part) {
				changed = true
			}
		}
	}
	return changed
}

func normalizeTransformersMediaPart(part map[string]any) bool {
	partType, _ := part["type"].(string)
	url, _ := part["url"].(string)
	if url == "" {
		return false
	}

	switch partType {
	case "image":
		part["type"] = "image_url"
		part["image_url"] = map[string]any{"url": url}
	case "video":
		part["type"] = "video_url"
		part["video_url"] = map[string]any{"url": url}
	case "audio":
		part["type"] = "audio_url"
		part["audio_url"] = map[string]any{"url": url}
	default:
		return false
	}
	delete(part, "url")
	return true
}

func isSGLangModel(modelCfg config.Model) bool {
	return strings.Contains(strings.ToLower(modelCfg.Run), "sglang")
}

func writeQueueError(w http.ResponseWriter, err error) {
	message := "request queue is full"
	if errors.Is(err, ErrQueueTimeout) {
		message = "request waited longer than the configured queue timeout"
	}
	protocol.WriteOpenAIError(w, http.StatusTooManyRequests, "queue_full", message)
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

func (s *Server) observeNoReady(model string, requestID string, decision ScheduleDecision) {
	if decision.MaxLoaded <= 0 || decision.ReadyReplicas > 0 {
		return
	}
	stats := QueueAcquireStats{Result: QueueResultNoReady}
	replicas := queueReplicaStats{
		readyReplicas:    decision.ReadyReplicas,
		occupiedReplicas: decision.OccupiedReplicas,
		maxLoaded:        decision.MaxLoaded,
	}
	s.observeQueue(model, requestID, "model", "model:"+model, stats, replicas)
}

func (s *Server) observeQueue(model, requestID, keyType, key string, stats QueueAcquireStats, replicas queueReplicaStats) {
	if stats.Result == "" {
		return
	}
	wait := time.Duration(stats.WaitMS) * time.Millisecond
	if s.metrics != nil {
		s.metrics.ObserveQueueWait(model, keyType, stats.Result, wait)
	}
	if s.pressure != nil {
		s.pressure.RecordQueue(PressureQueueObservation{
			Time:             time.Now(),
			Model:            model,
			Result:           stats.Result,
			WaitMS:           stats.WaitMS,
			ReadyReplicas:    replicas.readyReplicas,
			OccupiedReplicas: replicas.occupiedReplicas,
			ActiveBefore:     stats.ActiveBefore,
			QueuedBefore:     stats.QueuedBefore,
		})
	}
	s.logEvent("queue_observation", map[string]any{
		"request_id":        requestID,
		"model":             model,
		"key_type":          keyType,
		"key":               key,
		"result":            stats.Result,
		"wait_ms":           stats.WaitMS,
		"waited":            stats.Waited,
		"active":            stats.ActiveBefore,
		"queued":            stats.QueuedBefore,
		"max_concurrency":   stats.MaxConcurrency,
		"max_queue":         stats.MaxQueue,
		"ready_replicas":    replicas.readyReplicas,
		"occupied_replicas": replicas.occupiedReplicas,
		"max_loaded":        replicas.maxLoaded,
	})
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

func (s *Server) proxyAttempt(w http.ResponseWriter, r *http.Request, body []byte, model string, worker Worker, llamaSwapToken string) (bool, *proxyDispatchFailure, error, int, RequestLogEntry) {
	upstreamURL, err := upstreamRequestURL(worker.LlamaSwapURL, r.URL)
	if err != nil {
		s.recordReverseAccessResult(worker.ID, err, time.Now())
		return true, workerUnavailableFailure(), err, 0, RequestLogEntry{}
	}
	entry := RequestLogEntry{UpstreamURL: upstreamURL}

	req, err := http.NewRequestWithContext(r.Context(), r.Method, upstreamURL, bytes.NewReader(body))
	if err != nil {
		return true, workerUnavailableFailure(), err, 0, entry
	}
	copyRequestHeaders(req.Header, r.Header)
	req.Header.Set("X-Request-Id", requestIDFromHeader(r))
	req.Header.Set("X-Gateway-Model", model)
	req.Header.Set("X-Gateway-Worker", worker.ID)
	if llamaSwapToken != "" {
		req.Header.Set("Authorization", "Bearer "+llamaSwapToken)
	} else {
		req.Header.Del("Authorization")
	}
	req.ContentLength = int64(len(body))

	resp, err := defaultProxyHTTPClient.Do(req)
	if err != nil {
		s.recordReverseAccessResult(worker.ID, err, time.Now())
		if retryableProxyError(err, r.Context()) {
			return true, workerUnavailableFailure(), err, 0, entry
		}
		return false, nil, err, 0, entry
	}
	defer resp.Body.Close()
	s.recordReverseAccessResult(worker.ID, nil, time.Now())

	if retryableUpstreamStatus(resp.StatusCode) {
		_, _ = io.Copy(io.Discard, resp.Body)
		return true, upstreamRetryExhaustedFailure(resp.StatusCode), nil, resp.StatusCode, entry
	}
	if resp.StatusCode == http.StatusNotFound {
		respBody, err := io.ReadAll(resp.Body)
		if err != nil {
			return false, nil, err, resp.StatusCode, entry
		}
		if retryablePlatform404(resp.Header, respBody) {
			return true, upstreamRetryExhaustedFailure(resp.StatusCode), nil, resp.StatusCode, entry
		}

		copyResponseHeaders(w.Header(), resp.Header)
		w.WriteHeader(resp.StatusCode)
		_, _ = w.Write(respBody)
		entry.ResponseBytes = int64(len(respBody))
		parseOpenAIResponseLog(respBody, &entry)
		return false, nil, nil, resp.StatusCode, entry
	}

	copyResponseHeaders(w.Header(), resp.Header)
	w.WriteHeader(resp.StatusCode)
	respBody, err := copyResponseBody(w, resp.Body)
	if err != nil {
		return false, nil, nil, resp.StatusCode, entry
	}
	entry.ResponseBytes = int64(len(respBody))
	parseOpenAIResponseLog(respBody, &entry)
	return false, nil, nil, resp.StatusCode, entry
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

func copyResponseBody(w http.ResponseWriter, body io.Reader) ([]byte, error) {
	flusher, _ := w.(http.Flusher)
	buf := make([]byte, 32*1024)
	var captured bytes.Buffer
	for {
		n, readErr := body.Read(buf)
		if n > 0 {
			_, _ = captured.Write(buf[:n])
			if _, err := w.Write(buf[:n]); err != nil {
				return captured.Bytes(), err
			}
			if flusher != nil {
				flusher.Flush()
			}
		}
		if readErr != nil {
			if readErr == io.EOF {
				return captured.Bytes(), nil
			}
			return captured.Bytes(), readErr
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
