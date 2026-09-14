package wssrv

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
)

var quietLogger = slog.New(slog.NewTextHandler(io.Discard, nil))

func TestMarshalEnvelope(t *testing.T) {
	// no-payload event: data field omitted
	b, err := marshalEnvelope("PREPARE", nil)
	if err != nil {
		t.Fatal(err)
	}
	if want := `{"type":"PREPARE"}`; string(b) != want {
		t.Fatalf("无载荷信封 = %s, want %s", b, want)
	}

	// string payload (LOGIN)
	b, _ = marshalEnvelope("LOGIN", "玩家一")
	var env envelope
	if err := json.Unmarshal(b, &env); err != nil {
		t.Fatal(err)
	}
	if env.Type != "LOGIN" || string(env.Data) != `"玩家一"` {
		t.Fatalf("字符串载荷信封 = %s", b)
	}

	// object payload
	b, _ = marshalEnvelope("SITDOWN", map[string]int{"deskId": 3, "posId": 7})
	if !strings.Contains(string(b), `"deskId":3`) || !strings.Contains(string(b), `"posId":7`) {
		t.Fatalf("对象载荷信封 = %s", b)
	}
}

func waitEvent(t *testing.T, c *websocket.Conn) envelope {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, data, err := c.Read(ctx)
	if err != nil {
		t.Fatalf("读取下行事件失败: %v", err)
	}
	var env envelope
	if err := json.Unmarshal(data, &env); err != nil {
		t.Fatalf("下行信封解析失败: %q", data)
	}
	return env
}

func dial(t *testing.T, url string) *websocket.Conn {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	c, _, err := websocket.Dial(ctx, url, nil)
	if err != nil {
		t.Fatalf("连接 %s 失败: %v", url, err)
	}
	return c
}

func TestServeLifecycle(t *testing.T) {
	srv := New(quietLogger)
	var (
		mu       sync.Mutex
		gotLogin string
		gotSit   string
		disc     int
	)
	srv.OnEvent("LOGIN", func(c Conn, data json.RawMessage) {
		var name string
		if err := json.Unmarshal(data, &name); err != nil {
			t.Errorf("LOGIN 载荷解析失败: %v", err)
		}
		mu.Lock()
		gotLogin = name
		mu.Unlock()
		c.Emit("LOGIN_SUCCESS", []string{"中文"})
	})
	srv.OnEvent("SITDOWN", func(c Conn, data json.RawMessage) {
		mu.Lock()
		gotSit = string(data)
		mu.Unlock()
		c.Emit("PREPARE", nil)
	})
	srv.OnDisconnect(func(Conn) {
		mu.Lock()
		disc++
		mu.Unlock()
	})

	hs := httptest.NewServer(http.HandlerFunc(srv.ServeHTTP))
	defer hs.Close()
	url := "ws" + strings.TrimPrefix(hs.URL, "http")

	c := dial(t, url)
	defer c.CloseNow()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := c.Write(ctx, websocket.MessageText, []byte(`{"type":"LOGIN","data":"张三"}`)); err != nil {
		t.Fatal(err)
	}
	env := waitEvent(t, c)
	if env.Type != "LOGIN_SUCCESS" || string(env.Data) != `["中文"]` {
		t.Fatalf("LOGIN_SUCCESS 信封 = %+v", env)
	}

	if err := c.Write(ctx, websocket.MessageText, []byte(`{"type":"SITDOWN","data":{"deskId":1,"posId":2}}`)); err != nil {
		t.Fatal(err)
	}
	env = waitEvent(t, c)
	if env.Type != "PREPARE" || len(env.Data) != 0 {
		t.Fatalf("无载荷信封 = %+v", env)
	}

	// graceful close must trigger OnDisconnect exactly once
	if err := c.Close(websocket.StatusNormalClosure, ""); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		d := disc
		mu.Unlock()
		if d == 1 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	if disc != 1 {
		t.Fatalf("OnDisconnect 次数 = %d, want 1", disc)
	}
	if gotLogin != "张三" || gotSit != `{"deskId":1,"posId":2}` {
		t.Fatalf("上行载荷: login=%q sit=%q", gotLogin, gotSit)
	}
}

// TestAbruptDisconnect: dropping TCP without a close frame (kill / network
// loss) must still trigger OnDisconnect promptly.
func TestAbruptDisconnect(t *testing.T) {
	srv := New(quietLogger)
	done := make(chan struct{})
	srv.OnEvent("PING", func(c Conn, _ json.RawMessage) { c.Emit("PONG", nil) })
	srv.OnDisconnect(func(Conn) { close(done) })

	hs := httptest.NewServer(http.HandlerFunc(srv.ServeHTTP))
	defer hs.Close()
	c := dial(t, "ws"+strings.TrimPrefix(hs.URL, "http"))

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := c.Write(ctx, websocket.MessageText, []byte(`{"type":"PING"}`)); err != nil {
		t.Fatal(err)
	}
	waitEvent(t, c)

	c.CloseNow() // raw TCP drop, no close frame
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("异常断开未触发 OnDisconnect")
	}
}

// TestMalformedIgnored: unregistered events and malformed envelopes must not disconnect.
func TestMalformedIgnored(t *testing.T) {
	srv := New(quietLogger)
	srv.OnEvent("OK", func(c Conn, _ json.RawMessage) { c.Emit("OK", nil) })
	hs := httptest.NewServer(http.HandlerFunc(srv.ServeHTTP))
	defer hs.Close()
	c := dial(t, "ws"+strings.TrimPrefix(hs.URL, "http"))
	defer c.CloseNow()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	for _, frame := range []string{`not json`, `{"type":""}`, `{"type":"UNKNOWN","data":1}`, `{"type":"OK"}`} {
		if err := c.Write(ctx, websocket.MessageText, []byte(frame)); err != nil {
			t.Fatal(err)
		}
	}
	if env := waitEvent(t, c); env.Type != "OK" {
		t.Fatalf("处理完坏帧后应仍能收事件, got %s", env.Type)
	}
}
