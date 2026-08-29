package sc2kit

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"google.golang.org/protobuf/proto"

	"github.com/aiseeq/sc2kit/api"
)

// fakeSC2 is a minimal in-process stand-in for the SC2 client: a websocket
// server on /sc2api answering Ping and rejecting everything else.
func fakeSC2(t *testing.T) (addr string, closeFn func()) {
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
			var resp *api.Response
			if req.GetPing() != nil {
				resp = &api.Response{
					Response: &api.Response_Ping{Ping: &api.ResponsePing{
						GameVersion: proto.String("5.0.14.12345"),
					}},
					Status: api.Status_launched.Enum(),
				}
			} else if sr := req.GetStartReplay(); sr != nil {
				r := &api.ResponseStartReplay{}
				if len(sr.GetReplayData()) == 0 || len(sr.GetMapData()) == 0 {
					r.Error = api.ResponseStartReplay_InvalidReplayData.Enum()
					r.ErrorDetails = proto.String("empty replay or map data")
				}
				resp = &api.Response{
					Response: &api.Response_StartReplay{StartReplay: r},
					Status:   api.Status_in_replay.Enum(),
				}
			} else {
				resp = &api.Response{Error: []string{"unsupported request"}}
			}
			out, err := proto.Marshal(resp)
			if err != nil {
				t.Errorf("marshal response: %v", err)
				return
			}
			if err := conn.WriteMessage(websocket.BinaryMessage, out); err != nil {
				return
			}
		}
	}))
	return strings.TrimPrefix(srv.URL, "http://"), srv.Close
}

func TestPing(t *testing.T) {
	addr, closeFn := fakeSC2(t)
	defer closeFn()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	c, err := Dial(ctx, addr)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer c.Close()

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
	addr, closeFn := fakeSC2(t)
	defer closeFn()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	c, err := Dial(ctx, addr)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer c.Close()

	_, err = c.Request(ctx, &api.Request{
		Request: &api.Request_Quit{Quit: &api.RequestQuit{}},
	})
	if err == nil || !strings.Contains(err.Error(), "unsupported request") {
		t.Errorf("Request error = %v, want protocol error mentioning 'unsupported request'", err)
	}
}

func TestStartReplay(t *testing.T) {
	addr, closeFn := fakeSC2(t)
	defer closeFn()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	c, err := Dial(ctx, addr)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer c.Close()

	ok := &api.RequestStartReplay{
		Replay:  &api.RequestStartReplay_ReplayData{ReplayData: []byte("replay")},
		MapData: []byte("map"),
	}
	if err := c.StartReplay(ctx, ok); err != nil {
		t.Fatalf("StartReplay: %v", err)
	}

	bad := &api.RequestStartReplay{Replay: &api.RequestStartReplay_ReplayData{}}
	err = c.StartReplay(ctx, bad)
	if err == nil {
		t.Fatal("StartReplay с пустыми данными должен вернуть ошибку")
	}
	if !strings.Contains(err.Error(), "InvalidReplayData") {
		t.Fatalf("ошибка должна содержать код InvalidReplayData, got: %v", err)
	}
}

func TestDialRetryUntilDeadline(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 1200*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, err := Dial(ctx, "127.0.0.1:1") // nothing listens there
	if err == nil {
		t.Fatal("Dial to a dead port succeeded")
	}
	if elapsed := time.Since(start); elapsed < time.Second {
		t.Errorf("Dial gave up after %v, want it to retry until ctx deadline", elapsed)
	}
}
