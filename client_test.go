package sc2kit

import (
	"bufio"
	"context"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"google.golang.org/protobuf/proto"

	"github.com/aiseeq/sc2kit/api"
)

// answer is what the fake SC2 does with one request: reply with resp, stay
// silent (resp nil), or drop the socket without a reply (drop).
type answer struct {
	resp *api.Response
	drop bool
}

// serveSC2 is a minimal in-process stand-in for the SC2 client: a websocket
// server on /sc2api handing every request to handle. It returns host:port.
func serveSC2(t *testing.T, handle func(*api.Request) answer) string {
	t.Helper()
	up := websocket.Upgrader{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/sc2api" {
			http.NotFound(w, r)
			return
		}
		conn, err := up.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("upgrade: %v", err)
			return
		}
		defer conn.Close()
		for {
			_, data, err := conn.ReadMessage()
			if err != nil {
				return
			}
			req := &api.Request{}
			if err := proto.Unmarshal(data, req); err != nil {
				t.Errorf("unmarshal request: %v", err)
				return
			}
			a := handle(req)
			if a.drop {
				return
			}
			if a.resp == nil {
				continue
			}
			out, err := proto.Marshal(a.resp)
			if err != nil {
				t.Errorf("marshal response: %v", err)
				return
			}
			if err := conn.WriteMessage(websocket.BinaryMessage, out); err != nil {
				return
			}
		}
	}))
	t.Cleanup(srv.Close)
	return strings.TrimPrefix(srv.URL, "http://")
}

// fakeSC2 answers Ping and StartReplay, reports LeaveGame as "game already
// ended", drops the socket on Quit and rejects everything else.
func fakeSC2(t *testing.T) string {
	return serveSC2(t, func(req *api.Request) answer {
		switch {
		case req.GetPing() != nil:
			return answer{resp: &api.Response{
				Response: &api.Response_Ping{Ping: &api.ResponsePing{
					GameVersion: new("5.0.14.12345"),
				}},
				Status: api.Status_launched.Enum(),
			}}
		case req.GetStartReplay() != nil:
			sr := req.GetStartReplay()
			r := &api.ResponseStartReplay{}
			if len(sr.GetReplayData()) == 0 || len(sr.GetMapData()) == 0 {
				r.Error = api.ResponseStartReplay_InvalidReplayData.Enum()
				r.ErrorDetails = new("empty replay or map data")
			}
			return answer{resp: &api.Response{
				Response: &api.Response_StartReplay{StartReplay: r},
				Status:   api.Status_in_replay.Enum(),
			}}
		case req.GetLeaveGame() != nil:
			return answer{resp: &api.Response{Error: []string{"Not supported if game has already ended"}, Status: api.Status_ended.Enum()}}
		case req.GetQuit() != nil:
			return answer{drop: true} // SC2 drops the socket on Quit without answering
		default:
			return answer{resp: &api.Response{Error: []string{"unsupported request"}}}
		}
	})
}

// dialSC2 connects to addr; the client and its 5 s context end with the test.
func dialSC2(t *testing.T, addr string) (*Client, context.Context) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	c, err := Dial(ctx, addr)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c, ctx
}

// setDialRetryInterval shortens Dial's retry pause for one test.
func setDialRetryInterval(t *testing.T, d time.Duration) {
	t.Helper()
	old := dialRetryInterval
	dialRetryInterval = d
	t.Cleanup(func() { dialRetryInterval = old })
}

func TestPing(t *testing.T) {
	c, ctx := dialSC2(t, fakeSC2(t))

	ping, err := c.Ping(ctx)
	if err != nil {
		t.Fatalf("Ping: %v", err)
	}
	if got := ping.GetGameVersion(); got != "5.0.14.12345" {
		t.Errorf("GameVersion = %q, want 5.0.14.12345", got)
	}
	if got := c.Status(); got != api.Status_launched {
		t.Errorf("Status = %v, want launched", got)
	}
}

func TestResponseErrors(t *testing.T) {
	c, ctx := dialSC2(t, fakeSC2(t))

	resp, err := c.Request(ctx, &api.Request{
		Request: &api.Request_Debug{Debug: &api.RequestDebug{}},
	})
	if err == nil || !strings.Contains(err.Error(), "unsupported request") {
		t.Errorf("Request error = %v, want protocol error mentioning 'unsupported request'", err)
	}
	if got := resp.GetError(); len(got) != 1 || got[0] != "unsupported request" {
		t.Errorf("Request response errors = %q, want the response returned together with the error", got)
	}
	err = c.LeaveGame(ctx)
	if !errors.Is(err, ErrGameEnded) {
		t.Errorf("LeaveGame after end = %v, want ErrGameEnded", err)
	}
	var pe *ProtocolError
	if !errors.As(err, &pe) || pe.Status != api.Status_ended {
		t.Errorf("LeaveGame error = %v, want ProtocolError with status ended", err)
	}
}

// A request whose answer never arrived leaves the stream out of step: every
// later request must fail with ErrConnectionClosed instead of reading a stale
// response; after status ended the failure reads as ErrGameEnded.
func TestBrokenConnectionStaysBroken(t *testing.T) {
	c, ctx := dialSC2(t, fakeSC2(t))

	if err := c.LeaveGame(ctx); !errors.Is(err, ErrGameEnded) { // moves status to ended
		t.Fatalf("LeaveGame: %v", err)
	}
	err := c.Quit(ctx) // server drops the socket without an answer
	if !errors.Is(err, ErrGameEnded) {
		t.Fatalf("Quit after ended = %v, want ErrGameEnded", err)
	}
	if _, err := c.Ping(ctx); !errors.Is(err, ErrConnectionClosed) {
		t.Fatalf("Ping after broken socket = %v, want ErrConnectionClosed", err)
	}
}

func TestStartReplay(t *testing.T) {
	c, ctx := dialSC2(t, fakeSC2(t))

	ok := &api.RequestStartReplay{
		Replay:  &api.RequestStartReplay_ReplayData{ReplayData: []byte("replay")},
		MapData: []byte("map"),
	}
	if err := c.StartReplay(ctx, ok); err != nil {
		t.Fatalf("StartReplay: %v", err)
	}

	bad := &api.RequestStartReplay{Replay: &api.RequestStartReplay_ReplayData{}}
	err := c.StartReplay(ctx, bad)
	if err == nil {
		t.Fatal("StartReplay с пустыми данными должен вернуть ошибку")
	}
	if !strings.Contains(err.Error(), "InvalidReplayData") {
		t.Fatalf("ошибка должна содержать код InvalidReplayData, got: %v", err)
	}
}

func TestDialRetryUntilDeadline(t *testing.T) {
	const retryInterval = 20 * time.Millisecond
	const timeout = 120 * time.Millisecond
	setDialRetryInterval(t, retryInterval)

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	start := time.Now()
	_, err := Dial(ctx, "127.0.0.1:1") // nothing listens there
	if err == nil {
		t.Fatal("Dial to a dead port succeeded")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("Dial error = %v, want context deadline", err)
	}
	if elapsed := time.Since(start); elapsed < timeout-retryInterval {
		t.Errorf("Dial gave up after %v, want it to retry until ctx deadline", elapsed)
	}
}

// ErrConnectionClosed keeps the first transport failure in the chain (%w,
// 46f0cf1): a caller can still tell a read timeout from a dropped socket.
func TestBrokenConnectionKeepsCause(t *testing.T) {
	c, ctx := dialSC2(t, serveSC2(t, func(*api.Request) answer { return answer{} })) // never answers

	short, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	defer cancel()
	if _, err := c.Observation(short, &api.RequestObservation{}); !isTimeout(err) {
		t.Fatalf("Observation without an answer = %v, want read timeout", err)
	}
	_, err := c.Ping(ctx)
	if !errors.Is(err, ErrConnectionClosed) {
		t.Fatalf("Ping after timeout = %v, want ErrConnectionClosed", err)
	}
	if !isTimeout(err) {
		t.Errorf("Ping after timeout = %v, want the first failure (read timeout) in the chain", err)
	}
}

// isTimeout reports a network timeout anywhere in err's chain. gorilla hides
// the net package's timeout behind its own net.Error (hideTempErr), so
// os.ErrDeadlineExceeded is not in the chain.
func isTimeout(err error) bool {
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
}

// Both texts SC2 answers after the end of a game or replay unwrap to
// ErrGameEnded, whatever the case: replay walkers in sc2go and sc2demu stop
// on errors.Is(err, ErrGameEnded) and treat anything else as a failure.
func TestGameEndedTexts(t *testing.T) {
	for _, tc := range []struct {
		msg   string
		ended bool
	}{
		{"Game has already ended", true},
		{"Not supported if game has already ended", true},
		{"unsupported request", false},
	} {
		err := newProtocolError([]string{tc.msg}, api.Status_ended)
		if got := errors.Is(err, ErrGameEnded); got != tc.ended {
			t.Errorf("errors.Is(%q, ErrGameEnded) = %v, want %v", tc.msg, got, tc.ended)
		}
	}
}

// SC2 accepts connections only seconds after launch: Dial keeps retrying
// failed handshakes and returns the client once the server answers.
func TestDialRetriesUntilServerUp(t *testing.T) {
	setDialRetryInterval(t, 10*time.Millisecond)
	var attempts atomic.Int32
	up := websocket.Upgrader{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if attempts.Add(1) < 3 {
			http.Error(w, "starting", http.StatusServiceUnavailable)
			return
		}
		conn, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		_, _, _ = conn.ReadMessage() // until the client closes
	}))
	t.Cleanup(srv.Close)

	c, _ := dialSC2(t, strings.TrimPrefix(srv.URL, "http://"))
	if c == nil {
		t.Fatal("Dial returned no client")
	}
	if n := attempts.Load(); n != 3 {
		t.Errorf("handshake attempts = %d, want 3 (two refused, then accepted)", n)
	}
}

// wsFrame is the header of one websocket frame as it came over the wire.
type wsFrame struct {
	fin    bool
	opcode byte
	size   uint64
}

// readFrame reads one masked client frame and discards its payload.
func readFrame(r *bufio.Reader) (wsFrame, error) {
	var h [2]byte
	if _, err := io.ReadFull(r, h[:]); err != nil {
		return wsFrame{}, fmt.Errorf("frame header: %w", err)
	}
	f := wsFrame{fin: h[0]&0x80 != 0, opcode: h[0] & 0x0f, size: uint64(h[1] & 0x7f)}
	switch f.size {
	case 126:
		var ext [2]byte
		if _, err := io.ReadFull(r, ext[:]); err != nil {
			return wsFrame{}, fmt.Errorf("16-bit frame length: %w", err)
		}
		f.size = uint64(binary.BigEndian.Uint16(ext[:]))
	case 127:
		var ext [8]byte
		if _, err := io.ReadFull(r, ext[:]); err != nil {
			return wsFrame{}, fmt.Errorf("64-bit frame length: %w", err)
		}
		f.size = binary.BigEndian.Uint64(ext[:])
	}
	if h[1]&0x80 != 0 { // mask key
		if _, err := io.CopyN(io.Discard, r, 4); err != nil {
			return wsFrame{}, fmt.Errorf("frame mask key: %w", err)
		}
	}
	if _, err := io.CopyN(io.Discard, r, int64(f.size)); err != nil {
		return wsFrame{}, fmt.Errorf("frame payload (%d bytes): %w", f.size, err)
	}
	return f, nil
}

// SC2's websocket server does not reassemble fragmented messages and drops
// the connection (close 1006, caught 2026-08-29 on StartReplay with ~5 MB
// map_data): a large request must leave as one final frame. The fake here
// speaks raw websocket to see the frames gorilla would hide.
func TestLargeRequestOneFrame(t *testing.T) {
	frames := make(chan wsFrame, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, rw, err := http.NewResponseController(w).Hijack()
		if err != nil {
			t.Errorf("hijack: %v", err)
			return
		}
		defer conn.Close()
		sum := sha1.Sum([]byte(r.Header.Get("Sec-WebSocket-Key") + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"))
		fmt.Fprintf(rw, "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: %s\r\n\r\n",
			base64.StdEncoding.EncodeToString(sum[:]))
		if err := rw.Flush(); err != nil {
			return
		}
		f, err := readFrame(rw.Reader)
		if err != nil {
			t.Errorf("read frame: %v", err)
			return
		}
		frames <- f
		if !f.fin {
			return // like SC2: a fragment kills the connection
		}
		out, err := proto.Marshal(&api.Response{
			Response: &api.Response_StartReplay{StartReplay: &api.ResponseStartReplay{}},
			Status:   api.Status_in_replay.Enum(),
		})
		if err != nil || len(out) >= 126 {
			t.Errorf("response %d bytes, err %v: want a short frame", len(out), err)
			return
		}
		_, _ = rw.Write(append([]byte{0x82, byte(len(out))}, out...)) // FIN + binary, unmasked
		_ = rw.Flush()
		_, _ = readFrame(rw.Reader) // until the client closes
	}))
	t.Cleanup(srv.Close)

	c, ctx := dialSC2(t, strings.TrimPrefix(srv.URL, "http://"))
	req := &api.RequestStartReplay{
		Replay:  &api.RequestStartReplay_ReplayData{ReplayData: make([]byte, 1<<20)},
		MapData: make([]byte, 5<<20),
	}
	err := c.StartReplay(ctx, req)
	var f wsFrame
	select {
	case f = <-frames:
	case <-ctx.Done():
		t.Fatalf("server saw no frame; StartReplay: %v", err)
	}
	if !f.fin || f.opcode != websocket.BinaryMessage {
		t.Fatalf("first frame fin=%v opcode=%d size=%d: the request was fragmented", f.fin, f.opcode, f.size)
	}
	if want := proto.Size(&api.Request{Request: &api.Request_StartReplay{StartReplay: req}}); f.size != uint64(want) {
		t.Errorf("frame size = %d, want the whole request %d", f.size, want)
	}
	if err != nil {
		t.Errorf("StartReplay: %v", err)
	}
}
