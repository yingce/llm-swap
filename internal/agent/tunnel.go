package agent

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

const tunnelBodyChunkSize = 32 * 1024
const tunnelWriteWait = 10 * time.Second
const tunnelPongWait = 45 * time.Second
const tunnelPingInterval = 15 * time.Second

type TunnelClient struct {
	GatewayURL string
	AgentID    string
	Token      string
	LocalURL   string
	HTTPClient *http.Client
	Dialer     *websocket.Dialer
	Logf       func(format string, args ...any)
}

func (c TunnelClient) Run(ctx context.Context) error {
	backoff := time.Second
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		err := c.RunOnce(ctx)
		if err := ctx.Err(); err != nil {
			return err
		}
		if err == nil {
			backoff = time.Second
		} else if c.Logf != nil {
			c.Logf("agent tunnel reconnecting after error: %v", err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(backoff):
		}
		if backoff < 30*time.Second {
			backoff *= 2
		}
		if backoff > 30*time.Second {
			backoff = 30 * time.Second
		}
	}
}

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

func (c TunnelClient) RunOnce(ctx context.Context) error {
	if strings.TrimSpace(c.AgentID) == "" {
		return fmt.Errorf("agent id is required for tunnel")
	}
	wsURL := tunnelWebSocketURL(c.GatewayURL, c.AgentID)
	header := http.Header{}
	if c.Token != "" {
		header.Set("Authorization", "Bearer "+c.Token)
	}
	dialer := c.Dialer
	if dialer == nil {
		dialer = websocket.DefaultDialer
	}
	conn, _, err := dialer.DialContext(ctx, wsURL, header)
	if err != nil {
		return err
	}
	defer conn.Close()

	handled := 0
	var writeMu sync.Mutex
	done := make(chan struct{})
	defer close(done)
	if err := conn.SetReadDeadline(time.Now().Add(tunnelPongWait)); err != nil {
		return err
	}
	conn.SetPongHandler(func(string) error {
		return conn.SetReadDeadline(time.Now().Add(tunnelPongWait))
	})
	go tunnelKeepAlive(conn, &writeMu, done, tunnelPingInterval)
	inflight := newTunnelInflight()
	for {
		var req tunnelHTTPRequest
		if err := conn.ReadJSON(&req); err != nil {
			if handled > 0 || websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway) {
				return nil
			}
			return err
		}
		if req.ID == "" {
			continue
		}
		if req.Type == "http_cancel" {
			inflight.cancel(req.ID)
			continue
		}
		if req.Type != "http_request" {
			continue
		}
		handled++
		go func(req tunnelHTTPRequest) {
			reqCtx, cancel := context.WithCancel(ctx)
			inflight.add(req.ID, cancel)
			defer inflight.remove(req.ID)
			defer cancel()

			err := c.handleHTTPRequest(reqCtx, req, func(resp tunnelHTTPResponse) error {
				return writeTunnelJSON(conn, &writeMu, resp)
			})
			if err != nil && !errors.Is(ctx.Err(), context.Canceled) {
				_ = conn.Close()
			}
		}(req)
	}
}

func tunnelKeepAlive(conn *websocket.Conn, writeMu *sync.Mutex, done <-chan struct{}, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-done:
			return
		case <-ticker.C:
			writeMu.Lock()
			_ = conn.SetWriteDeadline(time.Now().Add(tunnelWriteWait))
			err := conn.WriteMessage(websocket.PingMessage, nil)
			writeMu.Unlock()
			if err != nil {
				_ = conn.Close()
				return
			}
		}
	}
}

func writeTunnelJSON(conn *websocket.Conn, writeMu *sync.Mutex, value any) error {
	writeMu.Lock()
	defer writeMu.Unlock()
	_ = conn.SetWriteDeadline(time.Now().Add(tunnelWriteWait))
	return conn.WriteJSON(value)
}

type tunnelInflight struct {
	mu      sync.Mutex
	cancels map[string]context.CancelFunc
}

func newTunnelInflight() *tunnelInflight {
	return &tunnelInflight{cancels: map[string]context.CancelFunc{}}
}

func (i *tunnelInflight) add(id string, cancel context.CancelFunc) {
	i.mu.Lock()
	i.cancels[id] = cancel
	i.mu.Unlock()
}

func (i *tunnelInflight) cancel(id string) {
	i.mu.Lock()
	cancel := i.cancels[id]
	i.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (i *tunnelInflight) remove(id string) {
	i.mu.Lock()
	delete(i.cancels, id)
	i.mu.Unlock()
}

func (c TunnelClient) handleHTTPRequest(ctx context.Context, msg tunnelHTTPRequest, send func(tunnelHTTPResponse) error) error {
	body, err := base64.StdEncoding.DecodeString(msg.BodyBase64)
	if err != nil {
		return sendTunnelHTTPError(send, msg.ID, err)
	}
	endpoint, err := localTunnelURL(c.LocalURL, msg.Path, msg.RawQuery)
	if err != nil {
		return sendTunnelHTTPError(send, msg.ID, err)
	}
	req, err := http.NewRequestWithContext(ctx, msg.Method, endpoint, bytes.NewReader(body))
	if err != nil {
		return sendTunnelHTTPError(send, msg.ID, err)
	}
	req.Header = msg.Header.Clone()
	req.ContentLength = int64(len(body))

	httpClient := c.HTTPClient
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	upstream, err := httpClient.Do(req)
	if err != nil {
		return sendTunnelHTTPError(send, msg.ID, err)
	}
	defer upstream.Body.Close()

	if err := send(tunnelHTTPResponse{
		Type:       "http_response_start",
		ID:         msg.ID,
		StatusCode: upstream.StatusCode,
		Header:     upstream.Header.Clone(),
	}); err != nil {
		return err
	}

	buf := make([]byte, tunnelBodyChunkSize)
	for {
		n, readErr := upstream.Body.Read(buf)
		if n > 0 {
			if err := send(tunnelHTTPResponse{
				Type:       "http_response_body",
				ID:         msg.ID,
				BodyBase64: base64.StdEncoding.EncodeToString(buf[:n]),
			}); err != nil {
				return err
			}
		}
		if readErr != nil {
			if readErr == io.EOF {
				return send(tunnelHTTPResponse{Type: "http_response_end", ID: msg.ID})
			}
			_ = send(tunnelHTTPResponse{Type: "http_response_end", ID: msg.ID, Error: readErr.Error()})
			return readErr
		}
	}
}

func sendTunnelHTTPError(send func(tunnelHTTPResponse) error, id string, err error) error {
	if err == nil {
		return nil
	}
	sendErr := send(tunnelHTTPResponse{Type: "http_response", ID: id, Error: err.Error()})
	if sendErr != nil {
		return sendErr
	}
	return nil
}

func tunnelWebSocketURL(gatewayURL string, agentID string) string {
	base, err := url.Parse(strings.TrimRight(gatewayURL, "/"))
	if err != nil {
		return ""
	}
	switch base.Scheme {
	case "https", "wss":
		base.Scheme = "wss"
	default:
		base.Scheme = "ws"
	}
	base.Path = path.Join(base.Path, "/internal/agent/tunnel")
	values := base.Query()
	values.Set("agent_id", agentID)
	base.RawQuery = values.Encode()
	return base.String()
}

func localTunnelURL(localBaseURL string, requestPath string, rawQuery string) (string, error) {
	base, err := url.Parse(strings.TrimRight(localBaseURL, "/"))
	if err != nil {
		return "", err
	}
	if base.Scheme == "" || base.Host == "" {
		return "", fmt.Errorf("local llama-swap url is invalid")
	}
	if !strings.HasPrefix(requestPath, "/") {
		requestPath = "/" + requestPath
	}
	base.Path = path.Join(base.Path, requestPath)
	base.RawQuery = rawQuery
	base.Fragment = ""
	return base.String(), nil
}
