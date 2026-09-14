// Package wssrv is the plain-WebSocket transport (github.com/coder/websocket).
// Protocol: single-layer JSON envelope {"type":"<event>","data":<payload>}
// (data may be omitted); event names and payloads match the old Socket.IO
// contract exactly (see hub/events.go).
package wssrv

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
)

const (
	pingInterval = 25 * time.Second // matches the Node engine.io default
	pingTimeout  = 60 * time.Second // network-drop fallback; TCP closes are seen by the read loop immediately
	writeTimeout = 10 * time.Second
	outBuffer    = 256
	maxFrame     = 1 << 16 // 64KB read limit; play payloads are far smaller
)

// Conn is the connection interface visible to the hub. It is a pointer type
// so instances compare directly (used by broadCastRoom to skip the sender).
type Conn interface {
	// Emit sends non-blockingly; v == nil sends an event without payload.
	// Frames to a dead connection are dropped silently and logged.
	Emit(event string, v any)
	Close()
}

// Handler receives the raw envelope "data" field; nil when the event has no payload.
type Handler func(c Conn, data json.RawMessage)

type Server struct {
	logger   *slog.Logger
	mu       sync.RWMutex
	handlers map[string]Handler
	onDisc   func(Conn)
	sidSeq   atomic.Int64
}

func New(logger *slog.Logger) *Server {
	if logger == nil {
		logger = slog.Default()
	}
	return &Server{logger: logger, handlers: make(map[string]Handler)}
}

// OnEvent and OnDisconnect must be called before serving starts.
func (s *Server) OnEvent(event string, fn Handler) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.handlers[event] = fn
}

func (s *Server) OnDisconnect(fn func(Conn)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.onDisc = fn
}

func (s *Server) handler(event string) Handler {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.handlers[event]
}

func (s *Server) disconnectHandler() func(Conn) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.onDisc
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// No origin check (matches the legacy CheckOrigin:true behavior).
	ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
	if err != nil {
		return
	}
	ws.SetReadLimit(maxFrame)

	ctx, cancel := context.WithCancel(context.Background())
	c := &conn{
		id:     fmt.Sprintf("c%d", s.sidSeq.Add(1)),
		srv:    s,
		ws:     ws,
		out:    make(chan []byte, outBuffer),
		cancel: cancel,
	}
	go c.writePump(ctx)
	c.readPump(ctx)
	c.shutdown()
}

type conn struct {
	id     string
	srv    *Server
	ws     *websocket.Conn
	out    chan []byte
	cancel context.CancelFunc
	once   sync.Once
}

// readPump reads and dispatches envelopes serially; any read error ends the connection.
func (c *conn) readPump(ctx context.Context) {
	for {
		typ, data, err := c.ws.Read(ctx)
		if err != nil {
			return
		}
		if typ != websocket.MessageText {
			continue // this protocol never uses binary frames
		}
		var env envelope
		if err := json.Unmarshal(data, &env); err != nil || env.Type == "" {
			c.srv.logger.Warn("连接收到无法解析的消息，忽略", "conn", c.id)
			continue
		}
		if h := c.srv.handler(env.Type); h != nil {
			h(c, env.Data)
		}
	}
}

// writePump sends frames from out serially and pings periodically to detect dead peers.
func (c *conn) writePump(ctx context.Context) {
	tick := time.NewTicker(pingInterval)
	defer tick.Stop()
	for {
		select {
		case frame := <-c.out:
			wctx, cancel := context.WithTimeout(ctx, writeTimeout)
			err := c.ws.Write(wctx, websocket.MessageText, frame)
			cancel()
			if err != nil {
				c.cancel() // unblocks the read loop; both funnel into shutdown
				return
			}
		case <-tick.C:
			pctx, cancel := context.WithTimeout(ctx, pingTimeout)
			err := c.ws.Ping(pctx)
			cancel()
			if err != nil {
				c.cancel()
				return
			}
		case <-ctx.Done():
			return
		}
	}
}

// Emit enqueues without blocking; a full queue means the peer is long gone,
// so the frame is dropped and logged.
func (c *conn) Emit(event string, v any) {
	frame, err := marshalEnvelope(event, v)
	if err != nil {
		c.srv.logger.Error("序列化载荷失败", "event", event, "err", err)
		return
	}
	select {
	case c.out <- frame:
	default:
		c.srv.logger.Warn("连接发送队列已满，丢弃事件", "conn", c.id, "event", event)
	}
}

func (c *conn) Close() { c.cancel() }

// shutdown closes the connection and fires OnDisconnect exactly once.
func (c *conn) shutdown() {
	c.once.Do(func() {
		c.cancel()
		c.ws.CloseNow()
		if fn := c.srv.disconnectHandler(); fn != nil {
			fn(c)
		}
	})
}

type envelope struct {
	Type string          `json:"type"`
	Data json.RawMessage `json:"data,omitempty"`
}

func marshalEnvelope(event string, v any) ([]byte, error) {
	if v == nil {
		return json.Marshal(envelope{Type: event})
	}
	data, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return json.Marshal(envelope{Type: event, Data: data})
}
