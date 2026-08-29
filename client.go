// Package sc2kit is a from-scratch Go library for the StarCraft II client API
// (Blizzard s2client-proto). Goal: complete, lazily-parsed access to
// everything the SC2 client streams.
package sc2kit

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"google.golang.org/protobuf/proto"

	"github.com/aiseeq/sc2kit/api"
)

// Client is a connection to a running SC2 client (websocket endpoint /sc2api).
// It is safe for concurrent use: requests are serialized with a mutex because
// the SC2 client processes one request at a time.
type Client struct {
	mu     sync.Mutex
	conn   *websocket.Conn
	status api.Status
}

// dialRetryInterval is how often Dial retries while the SC2 process is still
// starting up and not yet listening.
const dialRetryInterval = 500 * time.Millisecond

// Dial connects to an SC2 client at addr (host:port), retrying until the
// client accepts the connection or ctx is done. The game process needs a few
// seconds after launch before it starts listening, so pass a ctx with a
// deadline.
func Dial(ctx context.Context, addr string) (*Client, error) {
	url := "ws://" + addr + "/sc2api"
	// WriteBufferSize должен вмещать самое большое сообщение целиком: при
	// переполнении буфера gorilla фрагментирует сообщение на continuation-
	// фреймы, а websocket-сервер SC2 фрагментацию не поддерживает и рвёт
	// соединение (close 1006; поймано 2026-08-29 на StartReplay с map_data
	// ~5 МБ). 32 МБ покрывают replay_data+map_data самых больших реплеев.
	dialer := websocket.Dialer{HandshakeTimeout: 5 * time.Second, WriteBufferSize: 32 << 20}
	var lastErr error
	for {
		conn, resp, err := dialer.DialContext(ctx, url, nil)
		if err == nil {
			return &Client{conn: conn}, nil
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

// Close closes the connection. The SC2 client keeps running.
func (c *Client) Close() error {
	return c.conn.Close()
}

// Status returns the last game status reported by the SC2 client.
func (c *Client) Status() api.Status {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.status
}

// Request sends one request and waits for its response. A Response carrying
// protocol-level errors is returned together with a non-nil error — never
// silently.
func (c *Client) Request(ctx context.Context, req *api.Request) (*api.Response, error) {
	data, err := proto.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("sc2kit: marshal request: %w", err)
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	deadline, _ := ctx.Deadline() // zero time = no deadline, accepted by Set*Deadline
	if err := c.conn.SetWriteDeadline(deadline); err != nil {
		return nil, fmt.Errorf("sc2kit: set write deadline: %w", err)
	}
	if err := c.conn.WriteMessage(websocket.BinaryMessage, data); err != nil {
		return nil, fmt.Errorf("sc2kit: send request: %w", err)
	}
	if err := c.conn.SetReadDeadline(deadline); err != nil {
		return nil, fmt.Errorf("sc2kit: set read deadline: %w", err)
	}
	_, raw, err := c.conn.ReadMessage()
	if err != nil {
		return nil, fmt.Errorf("sc2kit: read response: %w", err)
	}

	resp := &api.Response{}
	if err := proto.Unmarshal(raw, resp); err != nil {
		return nil, fmt.Errorf("sc2kit: unmarshal response: %w", err)
	}
	c.status = resp.GetStatus()
	if len(resp.GetError()) > 0 {
		return resp, fmt.Errorf("sc2kit: response errors: %v", resp.GetError())
	}
	return resp, nil
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
