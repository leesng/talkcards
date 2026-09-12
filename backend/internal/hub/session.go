// session.go 已登录连接（会话）：身份归属、座位归属与串行发送队列，
// 以及会话注册表（在线会话的唯一索引）。所有方法调用方均持 hub 锁。
package hub

import (
	"talkcards/backend/internal/account"
	"talkcards/backend/internal/wssrv"
)

// Session deskID == -1 表示在大厅（js 中为空串）
type Session struct {
	conn   wssrv.Conn
	person *account.Person
	deskID int
	posID  int
	out    chan func() // 串行发送队列，见 hub 包注释
}

func (s *Session) UserName() string { return s.person.Name }

func (s *Session) pump() {
	for fn := range s.out {
		fn()
	}
}

// ---------- 会话注册表 ----------

// sessionRegistry 在线会话表：list 保持登录顺序供广播遍历，index 以连接为键 O(1) 定位。
// 非并发安全，由 hub 的互斥锁保护。
type sessionRegistry struct {
	list  []*Session
	index map[wssrv.Conn]*Session
}

func newSessionRegistry() *sessionRegistry {
	return &sessionRegistry{index: make(map[wssrv.Conn]*Session)}
}

// add 登记会话；同一连接二次登录时替换旧会话，避免注册表出现孤儿条目
func (r *sessionRegistry) add(c *Session) {
	if old := r.index[c.conn]; old != nil {
		r.removeFromList(old)
		close(old.out)
	}
	r.list = append(r.list, c)
	r.index[c.conn] = c
}

func (r *sessionRegistry) find(conn wssrv.Conn) *Session { return r.index[conn] }

// remove 摘除连接对应的会话并关闭其发送队列；不存在返回 nil
func (r *sessionRegistry) remove(conn wssrv.Conn) *Session {
	c := r.index[conn]
	if c == nil {
		return nil
	}
	delete(r.index, conn)
	r.removeFromList(c)
	close(c.out) // 已移出注册表，不会再有入队；pump 排空后自行退出
	return c
}

// byName 按用户名查找在线会话；不存在返回 nil
func (r *sessionRegistry) byName(name string) *Session {
	for _, c := range r.list {
		if c.UserName() == name {
			return c
		}
	}
	return nil
}

// each 按登录顺序遍历在线会话
func (r *sessionRegistry) each(fn func(*Session)) {
	for _, c := range r.list {
		fn(c)
	}
}

func (r *sessionRegistry) removeFromList(c *Session) {
	for i, x := range r.list {
		if x == c {
			r.list = append(r.list[:i], r.list[i+1:]...)
			return
		}
	}
}

// nextUID 会话内用户自增 id（当前无持久化身份）
func (h *Hub) nextUID() int64 {
	h.uidSeq++
	return h.uidSeq
}
