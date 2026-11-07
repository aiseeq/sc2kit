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
	"slices"
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
	return serveSC2Connection(t, func(conn *websocket.Conn) {
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
	})
}

func serveSC2Connection(t *testing.T, handle func(*websocket.Conn)) string {
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
		handle(conn)
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

func TestResponseWarning(t *testing.T) {
	warning := "observation was truncated"
	c, ctx := dialSC2(t, serveSC2(t, func(req *api.Request) answer {
		if req.GetObservation() != nil {
			messages := []string{warning}
			if req.GetObservation().GetGameLoop() == 1 {
				messages = []string{"Game has already ended"}
			}
			return answer{resp: &api.Response{
				Response: &api.Response_Observation{Observation: &api.ResponseObservation{
					Observation: &api.Observation{GameLoop: new(uint32(42))},
				}},
				Error:  messages,
				Status: api.Status_in_game.Enum(),
			}}
		}
		if req.GetAction() != nil {
			return answer{resp: &api.Response{
				Response: &api.Response_Action{Action: &api.ResponseAction{Result: []api.ActionResult{api.ActionResult_Success}}},
				Error:    []string{"some actions were skipped"},
			}}
		}
		return answer{resp: &api.Response{Error: []string{"request rejected"}}}
	}))
	type warningEvent struct {
		kind     string
		warnings []string
	}
	warnings := make(chan warningEvent, 2)
	c.SetWarningHandler(func(req *api.Request, messages []string) {
		warnings <- warningEvent{kind: fmt.Sprintf("%T", req.GetRequest()), warnings: messages}
	})

	obs, err := c.Observation(ctx, &api.RequestObservation{})
	if err != nil {
		t.Fatalf("Observation: %v", err)
	}
	if got := obs.GetObservation().GetGameLoop(); got != 42 {
		t.Errorf("observation game loop = %d, want 42", got)
	}
	act, err := c.Action(ctx, nil)
	if err != nil || len(act) != 1 || act[0] != api.ActionResult_Success {
		t.Errorf("Action result = %v, %v; want [Success]", act, err)
	}
	for _, want := range []warningEvent{
		{kind: "*api.Request_Observation", warnings: []string{warning}},
		{kind: "*api.Request_Action", warnings: []string{"some actions were skipped"}},
	} {
		select {
		case got := <-warnings:
			if got.kind != want.kind || !slices.Equal(got.warnings, want.warnings) {
				t.Errorf("warning event = %+v, want %+v", got, want)
			}
		case <-time.After(time.Second):
			t.Fatalf("warning handler did not receive %s warnings", want.kind)
		}
	}
	_, err = c.Observation(ctx, &api.RequestObservation{GameLoop: new(uint32(1))})
	if !errors.Is(err, ErrGameEnded) {
		t.Errorf("observation with game-ended response error = %v, want ErrGameEnded", err)
	}

	_, err = c.Request(ctx, &api.Request{Request: &api.Request_Debug{Debug: &api.RequestDebug{}}})
	var protocolErr *ProtocolError
	if !errors.As(err, &protocolErr) {
		t.Errorf("response without command error = %v, want ProtocolError", err)
	}
}

func TestCommandResponseKind(t *testing.T) {
	c, ctx := dialSC2(t, serveSC2(t, func(*api.Request) answer {
		return answer{resp: &api.Response{
			Response: &api.Response_Ping{Ping: &api.ResponsePing{}},
			Status:   api.Status_in_game.Enum(),
		}}
	}))

	if err := c.Debug(ctx); err == nil {
		t.Error("Debug accepted a ping response")
	}
	if err := c.LeaveGame(ctx); err == nil {
		t.Error("LeaveGame accepted a ping response")
	}
	if err := c.Quit(ctx); err == nil {
		t.Error("Quit accepted a ping response")
	}
	if err := c.Close(); err != nil {
		t.Fatalf("Close before Quit: %v", err)
	}
	if err := c.Quit(ctx); err == nil {
		t.Error("Quit after Close succeeded without sending the request")
	}

	c, ctx = dialSC2(t, serveSC2(t, func(*api.Request) answer { return answer{drop: true} }))
	if err := c.Quit(ctx); err != nil {
		t.Errorf("Quit when server closes without a response = %v, want success", err)
	}
}

// A request whose answer never arrived leaves the stream out of step: every
// later request must fail with ErrConnectionClosed instead of reading a stale
// response; transport failures retain their original cause regardless of status.
func TestBrokenConnectionStaysBroken(t *testing.T) {
	c, ctx := dialSC2(t, fakeSC2(t))

	if err := c.LeaveGame(ctx); !errors.Is(err, ErrGameEnded) { // moves status to ended
		t.Fatalf("LeaveGame: %v", err)
	}
	err := c.Quit(ctx) // server drops the socket without an answer
	if err != nil {
		t.Fatalf("Quit after ended = %v, want success on the expected socket close", err)
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

func TestDialPermanent(t *testing.T) {
	for _, addr := range []string{"127.0.0.1:not-a-port", "bad host:12345"} {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		start := time.Now()
		_, err := Dial(ctx, addr)
		cancel()
		if err == nil {
			t.Errorf("Dial with invalid address %q succeeded", addr)
			continue
		}
		if errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("Dial with invalid address %q waited for context deadline: %v", addr, err)
		}
		if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
			t.Errorf("Dial with invalid address %q took %v, want a prompt failure", addr, elapsed)
		}
	}
}

// ErrConnectionClosed keeps the first transport failure in the chain (%w,
// 46f0cf1): a caller can still tell a read timeout from a dropped socket.
func TestBrokenConnectionKeepsCause(t *testing.T) {
	c, ctx := dialSC2(t, serveSC2(t, func(*api.Request) answer { return answer{} })) // never answers

	short, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	defer cancel()
	if _, err := c.Observation(short, &api.RequestObservation{}); !isTimeout(err) || !errors.Is(err, context.DeadlineExceeded) {
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

func TestEndedReadError(t *testing.T) {
	endedResponse := func(req *api.Request) answer {
		if req.GetPing() != nil {
			return answer{resp: &api.Response{
				Response: &api.Response_Ping{Ping: &api.ResponsePing{}},
				Status:   api.Status_ended.Enum(),
			}}
		}
		return answer{}
	}
	c, ctx := dialSC2(t, serveSC2(t, endedResponse))

	if _, err := c.Ping(ctx); err != nil {
		t.Fatalf("Ping: %v", err)
	}
	short, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	defer cancel()
	_, err := c.SaveReplay(short)
	if !isTimeout(err) || !errors.Is(err, context.DeadlineExceeded) || !hasTransportTimeout(err) {
		t.Errorf("SaveReplay error = %v, want timeout and its transport cause", err)
	}
	if errors.Is(err, ErrGameEnded) {
		t.Errorf("SaveReplay error = %v, must not classify a read timeout as ErrGameEnded", err)
	}

	requestSeen := make(chan struct{})
	c, ctx = dialSC2(t, serveSC2(t, func(req *api.Request) answer {
		if req.GetPing() != nil {
			return endedResponse(req)
		}
		close(requestSeen)
		return answer{}
	}))
	if _, err := c.Ping(ctx); err != nil {
		t.Fatalf("Ping before cancellation: %v", err)
	}
	cancelCtx, cancelRequest := context.WithCancel(ctx)
	saveDone := make(chan error, 1)
	go func() {
		_, err := c.SaveReplay(cancelCtx)
		saveDone <- err
	}()
	select {
	case <-requestSeen:
	case <-time.After(time.Second):
		t.Fatal("server did not receive SaveReplay before cancellation")
	}
	cancelRequest()
	err = waitRequestError(t, saveDone, "canceled SaveReplay")
	if !errors.Is(err, context.Canceled) || errors.Is(err, ErrGameEnded) || !hasTransportTimeout(err) {
		t.Errorf("canceled SaveReplay error = %v, want cancellation and its transport cause, without ErrGameEnded", err)
	}

	c, ctx = dialSC2(t, serveSC2(t, func(req *api.Request) answer {
		if req.GetPing() != nil {
			return endedResponse(req)
		}
		return answer{drop: true}
	}))
	if _, err := c.Ping(ctx); err != nil {
		t.Fatalf("Ping before connection drop: %v", err)
	}
	_, err = c.SaveReplay(ctx)
	var closeErr *websocket.CloseError
	if errors.Is(err, ErrGameEnded) || !errors.As(err, &closeErr) {
		t.Errorf("SaveReplay after connection drop = %v, want transport error without ErrGameEnded", err)
	}
}

func TestRequestCancel(t *testing.T) {
	requests := make(chan struct{}, 3)
	c, ctx := dialSC2(t, serveSC2(t, func(*api.Request) answer {
		requests <- struct{}{}
		return answer{}
	}))

	firstCtx, cancelFirst := context.WithCancel(ctx)
	firstDone := make(chan error, 1)
	go func() {
		_, err := c.Request(firstCtx, &api.Request{Request: &api.Request_Ping{Ping: &api.RequestPing{}}})
		firstDone <- err
	}()
	select {
	case <-requests:
	case <-time.After(time.Second):
		t.Fatal("server did not receive first request")
	}

	secondCtx, cancelSecond := context.WithCancel(ctx)
	secondDone := make(chan error, 1)
	go func() {
		_, err := c.Request(secondCtx, &api.Request{Request: &api.Request_Ping{Ping: &api.RequestPing{}}})
		secondDone <- err
	}()
	select {
	case err := <-secondDone:
		t.Fatalf("queued request returned before cancellation: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	cancelSecond()
	if err := waitRequestError(t, secondDone, "queued request"); !errors.Is(err, context.Canceled) {
		t.Errorf("queued request error = %v, want context.Canceled", err)
	}
	cancelFirst()
	if err := waitRequestError(t, firstDone, "first request"); !errors.Is(err, context.Canceled) {
		t.Errorf("first request error = %v, want context.Canceled", err)
	}

	preCanceled, cancel := context.WithCancel(ctx)
	cancel()
	_, err := c.Request(preCanceled, &api.Request{Request: &api.Request_Ping{Ping: &api.RequestPing{}}})
	if !errors.Is(err, context.Canceled) {
		t.Errorf("pre-canceled request error = %v, want context.Canceled", err)
	}
	select {
	case <-requests:
		t.Error("pre-canceled request was sent")
	default:
	}
}

func TestRequestCancelDuringWrite(t *testing.T) {
	ready := make(chan struct{})
	release := make(chan struct{})
	defer close(release)
	addr := serveSC2Connection(t, func(conn *websocket.Conn) {
		close(ready)
		<-release // deliberately do not read client frames
		_ = conn.Close()
	})
	c, _ := dialSC2(t, addr)
	select {
	case <-ready:
	case <-time.After(time.Second):
		t.Fatal("server did not complete websocket handshake")
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := c.Request(ctx, &api.Request{Request: &api.Request_StartReplay{StartReplay: &api.RequestStartReplay{
			Replay: &api.RequestStartReplay_ReplayData{ReplayData: make([]byte, 30<<20)},
		}}})
		done <- err
	}()
	select {
	case err := <-done:
		t.Fatalf("large write completed before cancellation: %v", err)
	case <-time.After(500 * time.Millisecond):
	}
	cancel()
	err := waitRequestError(t, done, "write cancellation")
	if !errors.Is(err, context.Canceled) {
		t.Errorf("large request error = %v, want context.Canceled", err)
	}
}

func TestRequestCancelBeforeWrite(t *testing.T) {
	big := &api.Request{Request: &api.Request_StartReplay{StartReplay: &api.RequestStartReplay{
		Replay: &api.RequestStartReplay_ReplayData{ReplayData: make([]byte, 30<<20)},
	}}}
	release := make(chan struct{})
	defer close(release)
	addr := serveSC2Connection(t, func(conn *websocket.Conn) {
		<-release // keep the peer from reading any request
		_ = conn.Close()
	})

	for i := range 20 {
		c, _ := dialSC2(t, addr)
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() {
			_, err := c.Request(ctx, big)
			done <- err
		}()
		time.Sleep(time.Duration(i) * time.Millisecond)
		cancel()
		select {
		case err := <-done:
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("request canceled after %dms = %v, want context.Canceled", i, err)
			}
		case <-time.After(time.Second):
			_ = c.Close()
			t.Fatalf("request canceled after %dms remained blocked", i)
		}
		cancel()
		_ = c.Close()
	}
}

func waitRequestError(t *testing.T, done <-chan error, name string) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for %s", name)
		return nil
	}
}

// isTimeout reports a network timeout anywhere in err's chain. gorilla hides
// the net package's timeout behind its own net.Error (hideTempErr), so
// os.ErrDeadlineExceeded is not in the chain.
func isTimeout(err error) bool {
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
}

func hasTransportTimeout(err error) bool {
	if err == nil || err == context.DeadlineExceeded || err == context.Canceled {
		return false
	}
	if multi, ok := err.(interface{ Unwrap() []error }); ok {
		return slices.ContainsFunc(multi.Unwrap(), hasTransportTimeout)
	}
	if cause := errors.Unwrap(err); cause != nil {
		return hasTransportTimeout(cause)
	}
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
	frames := make(chan frameResult, 1)
	addr := rawFrameSC2Server(t, frames)
	c, ctx := dialSC2(t, addr)
	req := &api.RequestStartReplay{
		Replay:  &api.RequestStartReplay_ReplayData{ReplayData: make([]byte, 1<<20)},
		MapData: make([]byte, 5<<20),
	}
	err := c.StartReplay(ctx, req)
	var got frameResult
	select {
	case got = <-frames:
	case <-ctx.Done():
		t.Fatalf("server saw no frame; StartReplay: %v", err)
	}
	if got.err != nil {
		t.Fatalf("read request frame: %v", got.err)
	}
	f := got.frame
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

type frameResult struct {
	frame wsFrame
	err   error
}

func rawFrameSC2Server(t *testing.T, frames chan<- frameResult) string {
	t.Helper()
	return rawWebSocketServer(t, func(rw *bufio.ReadWriter) {
		f, err := readFrame(rw.Reader)
		frames <- frameResult{frame: f, err: err}
		if err != nil || !f.fin {
			return
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
	})
}

func rawWebSocketServer(t *testing.T, connected func(*bufio.ReadWriter)) string {
	t.Helper()
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
		connected(rw)
	}))
	t.Cleanup(srv.Close)
	return strings.TrimPrefix(srv.URL, "http://")
}

func TestOversizedRequest(t *testing.T) {
	frames := make(chan frameResult, 1)
	c, ctx := dialSC2(t, rawFrameSC2Server(t, frames))
	req := &api.RequestStartReplay{
		Replay: &api.RequestStartReplay_ReplayData{ReplayData: make([]byte, (32<<20)+1)},
	}
	err := c.StartReplay(ctx, req)
	if err == nil || !strings.Contains(err.Error(), "maximum request size") {
		t.Fatalf("oversized StartReplay error = %v, want explicit maximum-size error", err)
	}
	_ = c.Close()
	var got frameResult
	select {
	case got = <-frames:
	case <-ctx.Done():
		t.Fatalf("server did not observe connection close after rejected request: %v", ctx.Err())
	}
	if got.err == nil {
		t.Fatalf("server received a frame for rejected request: %+v", got.frame)
	}
}
