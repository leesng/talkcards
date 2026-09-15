// broadcast.go: under the lock only snapshot+enqueue; pumps send async.
package hub

import (
	"encoding/json"
	"strconv"
	"time"

	"talkcards/backend/internal/wssrv"
)

func (h *Hub) emit(c *Session, event string, v interface{}) {
	b, err := json.Marshal(v)
	if err != nil {
		h.logger.Error("序列化载荷失败", "event", event, "err", err)
		return
	}
	h.postRaw(c, event, b)
}

func (h *Hub) emitNoData(c *Session, event string) {
	h.post(c, func() { c.conn.Emit(event, nil) })
}

func (h *Hub) postRaw(c *Session, event string, b []byte) {
	h.post(c, func() { c.conn.Emit(event, json.RawMessage(b)) })
}

// post: non-blocking enqueue; a full queue means the receiver is long gone — drop and log.
func (h *Hub) post(c *Session, fn func()) {
	select {
	case c.out <- fn:
	default:
		h.logger.Warn("客户端发送队列已满，丢弃事件", "user", c.UserName())
	}
}

// broadCastHouse: lobby clients (js: empty deskId = in lobby).
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

// broadCastRoom: one desk; exclude may omit the initiator.
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

// broadCastGameStart: seated players get the full hands (bug-for-bug); desk
// spectators get the redacted payload — sizes only, no card faces.
func (h *Hub) broadCastGameStart(deskID int, full gameStartPayload) {
	fullBytes, err := json.Marshal(full)
	if err != nil {
		h.logger.Error("序列化载荷失败", "event", EvGameStart, "err", err)
		return
	}
	redactedBytes, err := json.Marshal(redactGameStart(full))
	if err != nil {
		h.logger.Error("序列化载荷失败", "event", EvGameStart, "err", err)
		return
	}
	h.sessions.each(func(c *Session) {
		if c.deskID != deskID {
			return
		}
		if c.posID == -1 {
			h.postRaw(c, EvGameStart, redactedBytes)
		} else {
			h.postRaw(c, EvGameStart, fullBytes)
		}
	})
}

func (h *Hub) nextID() int64 {
	h.msgSeq++
	return h.msgSeq
}

func now() string { return time.Now().Format("15:04:05") }

func itoa(n int) string { return strconv.Itoa(n) }
