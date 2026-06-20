package gateway

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"llm-swap/internal/config"
	"llm-swap/internal/protocol"
)

const maxProxyAttempts = 3

var defaultProxyHTTPClient = http.DefaultClient

func (s *Server) handleModelProxy(w http.ResponseWriter, r *http.Request) {
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

	exclude := make(map[string]bool)
	var lastDispatchFailure *proxyDispatchFailure
	for attempt := 0; attempt < maxProxyAttempts; attempt++ {
		if err := r.Context().Err(); err != nil {
			return
		}

		worker, err := (Scheduler{Config: s.config, Workers: s.workers}).Pick(model, time.Now(), exclude)
		if err != nil {
			break
		}
		exclude[worker.ID] = true

		workerRelease, ok := s.workers.Acquire(worker.ID, time.Now())
		if !ok {
			continue
		}
		tag := selectedWorkerTag(s.config, worker, model)
		accountingRelease := s.accounting.Acquire(r.Header.Get("X-Request-ID"), model, tag, worker.ID)
		release := releaseOnce(workerRelease, accountingRelease)

		retry, dispatchFailure, err := s.proxyAttempt(w, r, body, worker)
		release()
		if dispatchFailure != nil {
			lastDispatchFailure = dispatchFailure
		}
		if err != nil && r.Context().Err() != nil {
			return
		}
		if retry {
			continue
		}
		if err != nil {
			protocol.WriteOpenAIError(w, http.StatusBadGateway, "upstream_error", "upstream request failed")
		}
		return
	}

	if r.Context().Err() != nil {
		return
	}
	if lastDispatchFailure != nil {
		lastDispatchFailure.write(w)
		return
	}
	protocol.WriteOpenAIError(w, http.StatusServiceUnavailable, "no_healthy_worker", "no healthy worker is available for the requested model")
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

func (s *Server) proxyAttempt(w http.ResponseWriter, r *http.Request, body []byte, worker Worker) (bool, *proxyDispatchFailure, error) {
	upstreamURL, err := upstreamRequestURL(worker.LlamaSwapURL, r.URL)
	if err != nil {
		return true, workerUnavailableFailure(), err
	}

	req, err := http.NewRequestWithContext(r.Context(), r.Method, upstreamURL, bytes.NewReader(body))
	if err != nil {
		return true, workerUnavailableFailure(), err
	}
	copyRequestHeaders(req.Header, r.Header)
	if s.config.Tokens.LlamaSwap != "" {
		req.Header.Set("Authorization", "Bearer "+s.config.Tokens.LlamaSwap)
	} else {
		req.Header.Del("Authorization")
	}
	req.ContentLength = int64(len(body))

	resp, err := defaultProxyHTTPClient.Do(req)
	if err != nil {
		if retryableProxyError(err, r.Context()) {
			return true, workerUnavailableFailure(), err
		}
		return false, nil, err
	}
	defer resp.Body.Close()

	if retryableUpstreamStatus(resp.StatusCode) {
		_, _ = io.Copy(io.Discard, resp.Body)
		return true, upstreamRetryExhaustedFailure(resp.StatusCode), nil
	}

	copyResponseHeaders(w.Header(), resp.Header)
	w.WriteHeader(resp.StatusCode)
	if err := copyResponseBody(w, resp.Body); err != nil {
		return false, nil, nil
	}
	return false, nil, nil
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
