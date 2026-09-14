// session.go: sessions and registry; guarded by the hub lock.
package hub

import (
	"talkcards/backend/internal/account"
	"talkcards/backend/internal/wssrv"
)

// Session: deskID == -1 means in the lobby ("" in js).
type Session struct {
	conn   wssrv.Conn
	person *account.Person
	deskID int
	posID  int
	out    chan func() // serial send queue
}

func (s *Session) UserName() string { return s.person.Name }

func (s *Session) pump() {
	for fn := range s.out {
		fn()
	}
}

// sessionRegistry: list keeps login order; index is O(1) by connection.
// Not concurrency-safe; guarded by the hub mutex.
type sessionRegistry struct {
	list  []*Session
	index map[wssrv.Conn]*Session
}

func newSessionRegistry() *sessionRegistry {
	return &sessionRegistry{index: make(map[wssrv.Conn]*Session)}
}

// add: a second login on the same connection replaces the old session.
func (r *sessionRegistry) add(c *Session) {
	if old := r.index[c.conn]; old != nil {
		r.removeFromList(old)
		close(old.out)
	}
	r.list = append(r.list, c)
	r.index[c.conn] = c
}

func (r *sessionRegistry) find(conn wssrv.Conn) *Session { return r.index[conn] }

// remove unregisters and closes the send queue; nil if absent.
func (r *sessionRegistry) remove(conn wssrv.Conn) *Session {
	c := r.index[conn]
	if c == nil {
		return nil
	}
	delete(r.index, conn)
	r.removeFromList(c)
	close(c.out) // nothing more gets enqueued; pump drains and exits
	return c
}

func (r *sessionRegistry) byName(name string) *Session {
	for _, c := range r.list {
		if c.UserName() == name {
			return c
		}
	}
	return nil
}

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

func (h *Hub) nextUID() int64 {
	h.uidSeq++
	return h.uidSeq
}
