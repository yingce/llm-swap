package gateway

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

const agentTunnelWriteWait = 10 * time.Second
const agentTunnelPongWait = 45 * time.Second
const agentTunnelPingInterval = 15 * time.Second

type tunnelHTTPRequest struct {
	Type       string      `json:"type"`
	ID         string      `json:"id"`
	Method     string      `json:"method"`
	Path       string      `json:"path"`
	RawQuery   string      `json:"raw_query,omitempty"`
	Header     http.Header `json:"headers,omitempty"`
	BodyBase64 string      `json:"body_base64,omitempty"`
}

type tunnelHTTPResponse struct {
	Type       string      `json:"type"`
	ID         string      `json:"id"`
	StatusCode int         `json:"status_code"`
	Header     http.Header `json:"headers,omitempty"`
	BodyBase64 string      `json:"body_base64,omitempty"`
	Error      string      `json:"error,omitempty"`
}

type AgentTunnelRegistry struct {
	mu      sync.RWMutex
	tunnels map[string]*AgentTunnel
}

func NewAgentTunnelRegistry() *AgentTunnelRegistry {
	return &AgentTunnelRegistry{tunnels: map[string]*AgentTunnel{}}
}

func (r *AgentTunnelRegistry) Register(workerID string, conn *websocket.Conn) *AgentTunnel {
	tunnel := newAgentTunnel(workerID, conn)
	r.mu.Lock()
	if prev := r.tunnels[workerID]; prev != nil {
		prev.close()
	}
	r.tunnels[workerID] = tunnel
	r.mu.Unlock()
	return tunnel
}

func (r *AgentTunnelRegistry) Unregister(workerID string, tunnel *AgentTunnel) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.tunnels[workerID] == tunnel {
		delete(r.tunnels, workerID)
	}
}

func (r *AgentTunnelRegistry) Get(workerID string) (*AgentTunnel, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	tunnel := r.tunnels[workerID]
	return tunnel, tunnel != nil
}

type AgentTunnel struct {
	workerID string
	conn     *websocket.Conn
	writeMu  sync.Mutex

	mu      sync.Mutex
	pending map[string]chan tunnelHTTPResponse
	closed  bool
}

func newAgentTunnel(workerID string, conn *websocket.Conn) *AgentTunnel {
	return &AgentTunnel{
		workerID: workerID,
		conn:     conn,
		pending:  map[string]chan tunnelHTTPResponse{},
	}
}

func (t *AgentTunnel) Serve() {
	defer t.close()
	done := make(chan struct{})
	defer close(done)
	if err := t.conn.SetReadDeadline(time.Now().Add(agentTunnelPongWait)); err != nil {
		return
	}
	t.conn.SetPongHandler(func(string) error {
		return t.conn.SetReadDeadline(time.Now().Add(agentTunnelPongWait))
	})
	go t.keepAlive(done, agentTunnelPingInterval)
	for {
		var resp tunnelHTTPResponse
		if err := t.conn.ReadJSON(&resp); err != nil {
			return
		}
		if !validTunnelResponseType(resp.Type) || resp.ID == "" {
			continue
		}
		ch := t.pendingChannel(resp.ID)
		if ch == nil {
			continue
		}
		if !sendTunnelResponse(ch, resp) {
			return
		}
	}
}

func (t *AgentTunnel) keepAlive(done <-chan struct{}, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-done:
			return
		case <-ticker.C:
			t.writeMu.Lock()
			_ = t.conn.SetWriteDeadline(time.Now().Add(agentTunnelWriteWait))
			err := t.conn.WriteMessage(websocket.PingMessage, nil)
			t.writeMu.Unlock()
			if err != nil {
				t.close()
				return
			}
		}
	}
}

func (t *AgentTunnel) RoundTrip(ctx context.Context, requestID string, req *http.Request, body []byte) (tunnelHTTPResponse, error) {
	httpResp, err := t.RoundTripHTTP(ctx, requestID, req, body)
	if err != nil {
		return tunnelHTTPResponse{}, err
	}
	defer httpResp.Body.Close()
	respBody, err := io.ReadAll(httpResp.Body)
	if err != nil {
		return tunnelHTTPResponse{}, err
	}
	return tunnelHTTPResponse{
		Type:       "http_response",
		ID:         requestID,
		StatusCode: httpResp.StatusCode,
		Header:     httpResp.Header.Clone(),
		BodyBase64: base64.StdEncoding.EncodeToString(respBody),
	}, nil
}

func (t *AgentTunnel) RoundTripHTTP(ctx context.Context, requestID string, req *http.Request, body []byte) (*http.Response, error) {
	if requestID == "" {
		requestID = nextRequestID()
	}
	ch, err := t.addPending(requestID)
	if err != nil {
		return nil, err
	}

	msg := tunnelHTTPRequest{
		Type:       "http_request",
		ID:         requestID,
		Method:     req.Method,
		Path:       req.URL.Path,
		RawQuery:   req.URL.RawQuery,
		Header:     req.Header.Clone(),
		BodyBase64: base64.StdEncoding.EncodeToString(body),
	}
	t.writeMu.Lock()
	_ = t.conn.SetWriteDeadline(time.Now().Add(agentTunnelWriteWait))
	err = t.conn.WriteJSON(msg)
	t.writeMu.Unlock()
	if err != nil {
		t.removePending(requestID)
		return nil, err
	}

	select {
	case resp, ok := <-ch:
		if !ok {
			t.removePending(requestID)
			return nil, errTunnelClosed
		}
		if resp.Error != "" {
			t.removePending(requestID)
			return nil, errors.New(resp.Error)
		}
		switch resp.Type {
		case "http_response":
			t.removePending(requestID)
			respBody, err := base64.StdEncoding.DecodeString(resp.BodyBase64)
			if err != nil {
				return nil, err
			}
			return httpResponseFromTunnelMessage(req, resp, io.NopCloser(bytes.NewReader(respBody)), int64(len(respBody))), nil
		case "http_response_start":
			pr, pw := io.Pipe()
			body := &cancelingReadCloser{
				ReadCloser: pr,
				cancel: func() {
					_ = t.sendCancel(requestID)
				},
			}
			go t.copyTunnelResponseBody(ctx, requestID, ch, pw, body.markComplete)
			return httpResponseFromTunnelMessage(req, resp, body, -1), nil
		default:
			t.removePending(requestID)
			return nil, fmt.Errorf("unexpected tunnel response type %q", resp.Type)
		}
	case <-ctx.Done():
		_ = t.sendCancel(requestID)
		t.removePending(requestID)
		return nil, ctx.Err()
	}
}

func (t *AgentTunnel) sendCancel(requestID string) error {
	if requestID == "" {
		return nil
	}
	t.writeMu.Lock()
	defer t.writeMu.Unlock()
	_ = t.conn.SetWriteDeadline(time.Now().Add(agentTunnelWriteWait))
	return t.conn.WriteJSON(tunnelHTTPRequest{Type: "http_cancel", ID: requestID})
}

func (t *AgentTunnel) addPending(id string) (chan tunnelHTTPResponse, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return nil, errTunnelClosed
	}
	ch := make(chan tunnelHTTPResponse, 16)
	t.pending[id] = ch
	return ch, nil
}

func (t *AgentTunnel) pendingChannel(id string) chan tunnelHTTPResponse {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.pending[id]
}

func (t *AgentTunnel) removePending(id string) {
	t.mu.Lock()
	ch := t.pending[id]
	delete(t.pending, id)
	t.mu.Unlock()
	if ch != nil {
		close(ch)
	}
}

func (t *AgentTunnel) copyTunnelResponseBody(ctx context.Context, requestID string, ch <-chan tunnelHTTPResponse, pw *io.PipeWriter, markComplete func()) {
	defer t.removePending(requestID)
	for {
		select {
		case resp, ok := <-ch:
			if !ok {
				_ = pw.CloseWithError(errTunnelClosed)
				return
			}
			if resp.Error != "" {
				_ = pw.CloseWithError(errors.New(resp.Error))
				return
			}
			switch resp.Type {
			case "http_response_body":
				body, err := base64.StdEncoding.DecodeString(resp.BodyBase64)
				if err != nil {
					_ = pw.CloseWithError(err)
					return
				}
				if len(body) > 0 {
					if _, err := pw.Write(body); err != nil {
						_ = t.sendCancel(requestID)
						_ = pw.CloseWithError(err)
						return
					}
				}
			case "http_response_end":
				if markComplete != nil {
					markComplete()
				}
				_ = pw.Close()
				return
			case "http_response":
				body, err := base64.StdEncoding.DecodeString(resp.BodyBase64)
				if err != nil {
					_ = pw.CloseWithError(err)
					return
				}
				if len(body) > 0 {
					if _, err := pw.Write(body); err != nil {
						_ = t.sendCancel(requestID)
						_ = pw.CloseWithError(err)
						return
					}
				}
				if markComplete != nil {
					markComplete()
				}
				_ = pw.Close()
				return
			}
		case <-ctx.Done():
			_ = t.sendCancel(requestID)
			_ = pw.CloseWithError(ctx.Err())
			return
		}
	}
}

func validTunnelResponseType(typ string) bool {
	switch typ {
	case "http_response", "http_response_start", "http_response_body", "http_response_end":
		return true
	default:
		return false
	}
}

func sendTunnelResponse(ch chan tunnelHTTPResponse, resp tunnelHTTPResponse) (ok bool) {
	defer func() {
		if recover() != nil {
			ok = false
		}
	}()
	// Backpressure here is intentional: if the gateway/client reads slowly,
	// stop reading more tunnel frames for this connection.
	ch <- resp
	return true
}

func httpResponseFromTunnelMessage(req *http.Request, resp tunnelHTTPResponse, body io.ReadCloser, contentLength int64) *http.Response {
	statusCode := resp.StatusCode
	if statusCode == 0 {
		statusCode = http.StatusBadGateway
	}
	status := fmt.Sprintf("%d", statusCode)
	if text := http.StatusText(statusCode); text != "" {
		status = fmt.Sprintf("%d %s", statusCode, text)
	}
	return &http.Response{
		StatusCode:    statusCode,
		Status:        status,
		Header:        resp.Header.Clone(),
		Body:          body,
		ContentLength: contentLength,
		Request:       req,
	}
}

type cancelingReadCloser struct {
	io.ReadCloser
	cancel func()

	mu       sync.Mutex
	complete bool
	once     sync.Once
}

func (b *cancelingReadCloser) markComplete() {
	b.mu.Lock()
	b.complete = true
	b.mu.Unlock()
}

func (b *cancelingReadCloser) Close() error {
	b.mu.Lock()
	complete := b.complete
	b.mu.Unlock()
	if !complete && b.cancel != nil {
		b.once.Do(b.cancel)
	}
	return b.ReadCloser.Close()
}

func (t *AgentTunnel) close() {
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return
	}
	t.closed = true
	for id, ch := range t.pending {
		delete(t.pending, id)
		close(ch)
	}
	t.mu.Unlock()
	_ = t.conn.Close()
}

var errTunnelClosed = errors.New("agent tunnel is closed")

var agentTunnelUpgrader = websocket.Upgrader{
	CheckOrigin: func(*http.Request) bool { return true },
}

func (s *Server) handleAgentTunnel(w http.ResponseWriter, r *http.Request) {
	workerID := strings.TrimSpace(r.URL.Query().Get("agent_id"))
	if workerID == "" {
		http.Error(w, "agent_id is required", http.StatusBadRequest)
		return
	}
	conn, err := agentTunnelUpgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	tunnel := s.tunnels.Register(workerID, conn)
	defer s.tunnels.Unregister(workerID, tunnel)
	tunnel.Serve()
}
