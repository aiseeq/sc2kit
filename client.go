// Package sc2kit is a from-scratch Go library for the StarCraft II client API
// (Blizzard s2client-proto). Goal: complete, lazily-parsed access to
// everything the SC2 client streams.
package sc2kit

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"strconv"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"google.golang.org/protobuf/proto"

	"github.com/aiseeq/sc2kit/api"
)

// Client is a connection to a running SC2 client (websocket endpoint /sc2api).
// It is safe for concurrent use: requests are serialized because the SC2
// client processes one request at a time. Context cancellation interrupts
// queue waiting and network I/O, including writes while a WebSocket frame is
// being prepared.
type Client struct {
	mu             sync.Mutex
	queue          chan struct{}
	conn           *websocket.Conn
	transport      *cancelableConn
	status         api.Status
	broken         error // first transport failure; every later request fails with ErrConnectionClosed
	warningMu      sync.RWMutex
	warningHandler func(req *api.Request, warnings []string)
}

// dialRetryInterval is how often Dial retries while the SC2 process is still
// starting up and not yet listening.
var dialRetryInterval = 500 * time.Millisecond

const maxRequestSize = 32 << 20

// Dial connects to an SC2 client at addr (host:port), retrying network failures
// and server refusals until the client accepts the connection or ctx is done.
// Syntactically invalid host:port addresses return immediately. The game
// process needs a few seconds after launch before it starts listening, so pass
// a ctx with a deadline.
func Dial(ctx context.Context, addr string) (*Client, error) {
	url := "ws://" + addr + "/sc2api"
	if err := validateDialAddress(addr); err != nil {
		return nil, fmt.Errorf("sc2kit: dial %s: %w", url, err)
	}
	// WriteBufferSize должен вмещать любое разрешённое сообщение целиком: при
	// переполнении буфера gorilla фрагментирует сообщение на continuation-
	// фреймы, а websocket-сервер SC2 фрагментацию не поддерживает и рвёт
	// соединение (close 1006; поймано 2026-08-29 на StartReplay с map_data
	// ~5 МБ; тот же факт — go-sc2ai client/connection.go MaxMessageSize).
	// Запросы больше maxRequestSize отклоняются до записи.
	// Ping-фреймы websocket не шлём (gorilla сам их не шлёт): сервер SC2
	// не отвечает PONG, а разбирает ping как запрос и отдаёт пустой ответ
	// (Tyr SC2API-CSharp/ProtobufProxy.cs, Sharky FastProtobufProxy.cs —
	// оба ставят KeepAliveInterval в 30 дней).
	dialer := websocket.Dialer{HandshakeTimeout: 5 * time.Second, WriteBufferSize: maxRequestSize}
	var transport *cancelableConn
	dialer.NetDialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		conn, err := (&net.Dialer{}).DialContext(ctx, network, addr)
		if err != nil {
			return nil, err
		}
		transport = &cancelableConn{Conn: conn}
		return transport, nil
	}
	var lastErr error
	for {
		conn, resp, err := dialer.DialContext(ctx, url, nil)
		if err == nil {
			return &Client{conn: conn, transport: transport, queue: make(chan struct{}, 1)}, nil
		}
		if resp != nil {
			_ = resp.Body.Close()
		}
		lastErr = err
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("sc2kit: dial %s: %w (last attempt: %v)", url, ctx.Err(), lastErr)
		case <-time.After(dialRetryInterval):
		}
	}
}

// cancelableConn keeps a canceled write interrupted even when gorilla resets
// its per-frame write deadline while preparing a frame.
type cancelableConn struct {
	net.Conn
	writeMu       sync.Mutex
	writeCanceled bool
	writeErr      error
}

func (c *cancelableConn) SetWriteDeadline(deadline time.Time) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if c.writeCanceled {
		deadline = time.Now()
	}
	return c.Conn.SetWriteDeadline(deadline)
}

func (c *cancelableConn) Write(p []byte) (int, error) {
	c.writeMu.Lock()
	canceled := c.writeCanceled
	err := c.writeErr
	c.writeMu.Unlock()
	if canceled {
		if err == nil {
			err = context.Canceled
		}
		return 0, err
	}
	return c.Conn.Write(p)
}

func (c *cancelableConn) setWriteCancellation(canceled bool, err error, deadline time.Time) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	c.writeCanceled = canceled
	c.writeErr = err
	if canceled {
		deadline = time.Now()
	}
	return c.Conn.SetWriteDeadline(deadline)
}

func validateDialAddress(addr string) error {
	if _, err := url.ParseRequestURI("ws://" + addr + "/sc2api"); err != nil {
		return fmt.Errorf("invalid host:port %q: %w", addr, err)
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("invalid host:port %q: %w", addr, err)
	}
	if host == "" {
		return fmt.Errorf("invalid host:port %q: host is empty", addr)
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 {
		return fmt.Errorf("invalid host:port %q: port must be between 1 and 65535", addr)
	}
	return nil
}

// Close closes the connection. The SC2 client keeps running.
func (c *Client) Close() error {
	return c.conn.Close()
}

// SetWarningHandler sets the callback for warnings returned alongside command
// results. The callback receives the request and a copy of warning texts. If
// handler is nil, warnings are written to the default slog logger.
func (c *Client) SetWarningHandler(handler func(req *api.Request, warnings []string)) {
	c.warningMu.Lock()
	c.warningHandler = handler
	c.warningMu.Unlock()
}

func (c *Client) reportWarnings(req *api.Request, warnings []string) {
	c.warningMu.RLock()
	handler := c.warningHandler
	c.warningMu.RUnlock()
	if handler != nil {
		handler(req, append([]string(nil), warnings...))
		return
	}
	slog.Warn("SC2 response warnings", "request", fmt.Sprintf("%T", req.GetRequest()), "warnings", warnings)
}

// Status returns the last game status reported by the SC2 client.
func (c *Client) Status() api.Status {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.status
}

// Request sends one request and waits for its response. Context cancellation
// interrupts queue waits and network I/O; a canceled or expired context remains
// identifiable as context.Canceled or context.DeadlineExceeded, respectively.
// The marshaled request must not exceed 32 MiB; larger requests are rejected
// before any bytes are written so SC2 never receives a fragmented message.
// Unrecognized errors accompanying a command result are warnings retained in
// Response.error and delivered to the configured warning handler, or logged
// with slog if none is configured.
// Recognized protocol conditions and errors without a command result are
// returned as ProtocolError.
func (c *Client) Request(ctx context.Context, req *api.Request) (response *api.Response, requestErr error) {
	data, err := proto.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("sc2kit: marshal request: %w", err)
	}
	if len(data) > maxRequestSize {
		return nil, fmt.Errorf("sc2kit: request size %d exceeds maximum request size %d bytes", len(data), maxRequestSize)
	}
	var warnings []string
	defer func() {
		if len(warnings) > 0 {
			c.reportWarnings(req, warnings)
		}
	}()

	release, err := c.acquireRequest(ctx)
	if err != nil {
		return nil, err
	}
	defer release()

	c.mu.Lock()
	defer c.mu.Unlock()

	if c.broken != nil {
		return nil, fmt.Errorf("%w (after: %w)", ErrConnectionClosed, c.broken)
	}
	stopCancel, err := c.interruptRequestOnCancel(ctx)
	if err != nil {
		return nil, err
	}
	defer func() {
		if err := stopCancel(); err != nil {
			if c.broken == nil {
				c.broken = err
			}
			deadlineErr := &requestDeadlineError{err: err}
			if requestErr == nil {
				response = nil
				requestErr = fmt.Errorf("sc2kit: reset request deadlines: %w", deadlineErr)
			} else {
				requestErr = fmt.Errorf("%w: %w", requestErr, deadlineErr)
			}
		}
	}()
	if err := c.conn.WriteMessage(websocket.BinaryMessage, data); err != nil {
		c.broken = err
		if ctx.Err() != nil {
			return nil, fmt.Errorf("sc2kit: send request: %w: %w", ctx.Err(), err)
		}
		return nil, fmt.Errorf("sc2kit: send request: %w", err)
	}
	if err := ctx.Err(); err != nil {
		c.broken = err
		return nil, fmt.Errorf("sc2kit: read response: %w", err)
	}
	_, raw, err := c.conn.ReadMessage()
	if err != nil {
		// The answer to this request is still in flight (or the socket is
		// gone): the stream is out of step from here on.
		c.broken = err
		readErr := &responseReadError{err: err}
		if ctx.Err() != nil {
			return nil, fmt.Errorf("sc2kit: read response: %w: %w", ctx.Err(), readErr)
		}
		return nil, fmt.Errorf("sc2kit: read response: %w", readErr)
	}

	resp := &api.Response{}
	if err := proto.Unmarshal(raw, resp); err != nil {
		return nil, fmt.Errorf("sc2kit: unmarshal response: %w", err)
	}
	c.status = resp.GetStatus()
	if len(resp.GetError()) > 0 {
		protocolErr := newProtocolError(resp.GetError(), resp.GetStatus())
		if resp.GetResponse() == nil || protocolErr.known != nil {
			return resp, protocolErr
		}
		warnings = resp.GetError()
	}
	return resp, nil
}

func (c *Client) acquireRequest(ctx context.Context) (func(), error) {
	select {
	case c.queue <- struct{}{}:
		if err := ctx.Err(); err != nil {
			<-c.queue
			return nil, fmt.Errorf("sc2kit: request context: %w", err)
		}
		return func() { <-c.queue }, nil
	default:
	}
	select {
	case c.queue <- struct{}{}:
		if err := ctx.Err(); err != nil {
			<-c.queue
			return nil, fmt.Errorf("sc2kit: request context: %w", err)
		}
		return func() { <-c.queue }, nil
	case <-ctx.Done():
		return nil, fmt.Errorf("sc2kit: wait for request queue: %w", ctx.Err())
	}
}

func (c *Client) interruptRequestOnCancel(ctx context.Context) (func() error, error) {
	cancelDone := make(chan struct{})
	cancelErrors := make(chan error, 2)
	stop := context.AfterFunc(ctx, func() {
		if err := c.conn.SetReadDeadline(time.Now()); err != nil {
			cancelErrors <- err
		}
		if err := c.transport.setWriteCancellation(true, ctx.Err(), time.Time{}); err != nil {
			cancelErrors <- err
		}
		close(cancelDone)
	})
	return func() error {
		if !stop() {
			<-cancelDone
		}
		var firstErr error
		if err := c.conn.SetReadDeadline(time.Time{}); err != nil {
			firstErr = err
		}
		if err := c.transport.setWriteCancellation(false, nil, time.Time{}); err != nil && firstErr == nil {
			firstErr = err
		}
		select {
		case err := <-cancelErrors:
			if firstErr == nil {
				firstErr = err
			}
		default:
		}
		select {
		case err := <-cancelErrors:
			if firstErr == nil {
				firstErr = err
			}
		default:
		}
		return firstErr
	}, nil
}

// Ping asks the SC2 client for its build and version info.
func (c *Client) Ping(ctx context.Context) (*api.ResponsePing, error) {
	resp, err := c.Request(ctx, &api.Request{
		Request: &api.Request_Ping{Ping: &api.RequestPing{}},
	})
	if err != nil {
		return nil, fmt.Errorf("ping: %w", err)
	}
	ping := resp.GetPing()
	if ping == nil {
		return nil, fmt.Errorf("sc2kit: response is not a ping response: %v", resp)
	}
	return ping, nil
}
