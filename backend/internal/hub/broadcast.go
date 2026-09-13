// broadcast.go 发送原语：持锁路径只做载荷快照与入队，
// 实际 Emit 由各会话的 pump goroutine 串行执行。
package hub

import (
	"encoding/json"
	"strconv"
	"time"

	"talkcards/backend/internal/wssrv"
)

// emit 持锁期间序列化载荷快照并入队
func (h *Hub) emit(c *Session, event string, v interface{}) {
	b, err := json.Marshal(v)
	if err != nil {
		h.logger.Error("序列化载荷失败", "event", event, "err", err)
		return
	}
	h.postRaw(c, event, b)
}

// emitNoData 无载荷事件（js: socket.emit(event)）
func (h *Hub) emitNoData(c *Session, event string) {
	h.post(c, func() { c.conn.Emit(event, nil) })
}

func (h *Hub) postRaw(c *Session, event string, b []byte) {
	h.post(c, func() { c.conn.Emit(event, json.RawMessage(b)) })
}

// post 非阻塞入队；队列满只可能发生在接收方早已失联的场景，丢弃并记录
func (h *Hub) post(c *Session, fn func()) {
	select {
	case c.out <- fn:
	default:
		h.logger.Warn("客户端发送队列已满，丢弃事件", "user", c.UserName())
	}
}

// broadCastHouse 通知大厅中的客户端（js: deskId 为空串即在大厅）
func (h *Hub) broadCastHouse(event string, v interface{}) {
	b, err := json.Marshal(v)
	if err != nil {
		h.logger.Error("序列化载荷失败", "event", event, "err", err)
		return
	}
	h.sessions.each(func(c *Session) {
		if c.deskID == -1 {
			h.postRaw(c, event, b)
		}
	})
}

// broadCastRoom 通知同桌客户端，可排除发起者（exclude 传 nil 表示不排除）
func (h *Hub) broadCastRoom(event string, deskID int, v interface{}, exclude wssrv.Conn) {
	b, err := json.Marshal(v)
	if err != nil {
		h.logger.Error("序列化载荷失败", "event", event, "err", err)
		return
	}
	h.sessions.each(func(c *Session) {
		if c.deskID == deskID && c.conn != exclude {
			h.postRaw(c, event, b)
		}
	})
}

func (h *Hub) nextID() int64 {
	h.msgSeq++
	return h.msgSeq
}

func now() string { return time.Now().Format("15:04:05") }

// itoa 系统消息里的数字拼装（避免到处 strconv.Itoa）
func itoa(n int) string { return strconv.Itoa(n) }
