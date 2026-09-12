// Package wssrv 纯 WebSocket 通信层（github.com/coder/websocket），
// 取代原 go-socket.io fork。协议为单层 JSON 信封：
//
//	{"type":"<事件名>","data":<载荷>}   // data 可缺省（无载荷事件）
//
// 事件名与载荷字段和旧 Socket.IO 事件契约完全一致（见 hub/events.go），
// 前端仅换用 js/mySocket.js 的等价客户端，业务代码零改动。
//
// 生命周期：每个连接一个读循环（串行派发事件，对应 Node 单线程语义）与
// 一个写循环（串行发送 + 周期 ping）。任一方退出即触发唯一的 OnDisconnect。
// 客户端正常关闭、杀进程、断网（ping 超时兜底）均能即时回收会话。
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
	pingInterval = 25 * time.Second // 心跳间隔，对齐 Node engine.io 默认值
	pingTimeout  = 60 * time.Second // pong 等待上限（断网兜底；TCP 关闭由读循环即时感知）
	writeTimeout = 10 * time.Second
	outBuffer    = 256     // 单连接待发帧缓冲
	maxFrame     = 1 << 16 // 64KB 读上限，出牌载荷远小于此
)

// Conn hub 侧可见的连接接口。实现为指针类型，可直接比较（broadCastRoom 排除自身用）
type Conn interface {
	// Emit 非阻塞发送；v 为 nil 时发送无载荷事件。连接已死时静默丢弃并记日志
	Emit(event string, v any)
	// Close 主动断开（触发 OnDisconnect）
	Close()
}

// Handler 事件处理器；data 为信封 data 字段原文，无载荷时为 nil
type Handler func(c Conn, data json.RawMessage)

// Server WebSocket 服务端：事件路由 + 连接生命周期
type Server struct {
	logger   *slog.Logger
	mu       sync.RWMutex
	handlers map[string]Handler
	onDisc   func(Conn)
	sidSeq   atomic.Int64
}

// New 创建 WebSocket 服务端
func New(logger *slog.Logger) *Server {
	if logger == nil {
		logger = slog.Default()
	}
	return &Server{logger: logger, handlers: make(map[string]Handler)}
}

// OnEvent 注册事件处理器（须在开始服务前注册完毕）
func (s *Server) OnEvent(event string, fn Handler) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.handlers[event] = fn
}

// OnDisconnect 注册断开回调（须在开始服务前注册）
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

// ServeHTTP 处理一次 WebSocket 升级直至连接结束
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// 游戏服务器不做来源校验（对齐旧版 CheckOrigin:true）
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

// ---------- 连接实现 ----------

type conn struct {
	id     string
	srv    *Server
	ws     *websocket.Conn
	out    chan []byte
	cancel context.CancelFunc
	once   sync.Once
}

// readPump 读循环：串行读取并派发信封；任何读错误即结束连接
func (c *conn) readPump(ctx context.Context) {
	for {
		typ, data, err := c.ws.Read(ctx)
		if err != nil {
			return
		}
		if typ != websocket.MessageText {
			continue // 本协议不使用二进制帧
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

// writePump 写循环：串行发送 out 中的帧，并周期 ping 检测断网
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
				c.cancel() // 通知读循环退出，统一走 shutdown
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

// Emit 非阻塞入队；队列满说明接收方早已失联，丢弃并记录
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

// Close 由业务层主动断开：cancel 使读/写循环退出并触发 OnDisconnect
func (c *conn) Close() { c.cancel() }

// shutdown 收尾：保证一次性地关连接、触发断开回调
func (c *conn) shutdown() {
	c.once.Do(func() {
		c.cancel()
		c.ws.CloseNow()
		if fn := c.srv.disconnectHandler(); fn != nil {
			fn(c)
		}
	})
}

// ---------- 信封 ----------

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
