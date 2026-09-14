// Session registry tests.
package hub

import (
	"testing"

	"talkcards/backend/internal/account"
)

func newTestSession(conn *fakeConn, uid int64, name string, deskID int) *Session {
	return &Session{conn: conn, person: account.New(uid, name), deskID: deskID, posID: -1, out: make(chan func(), 1)}
}

func TestSessionRegistry(t *testing.T) {
	r := newSessionRegistry()
	c1, c2 := &fakeConn{id: "c1"}, &fakeConn{id: "c2"}
	s1 := newTestSession(c1, 1, "a", -1)
	s2 := newTestSession(c2, 2, "b", 1)
	r.add(s1)
	r.add(s2)

	if r.find(c1) != s1 || r.find(c2) != s2 {
		t.Fatalf("按连接查找应命中各自会话")
	}
	if r.byName("b") != s2 || r.byName("不存在") != nil {
		t.Fatalf("按名查找异常")
	}

	var order []string
	r.each(func(c *Session) { order = append(order, c.UserName()) })
	if len(order) != 2 || order[0] != "a" || order[1] != "b" {
		t.Fatalf("遍历应保持登录顺序，实际 %v", order)
	}

	if got := r.remove(c1); got != s1 {
		t.Fatalf("remove 应返回被移除会话")
	}
	if r.find(c1) != nil || r.byName("a") != nil {
		t.Fatalf("移除后不应再可查")
	}
	if _, ok := <-s1.out; ok {
		t.Fatalf("移除后发送队列应已关闭")
	}
	if r.remove(c1) != nil {
		t.Fatalf("重复移除应返回 nil")
	}

	// Second login on the same connection replaces the old session.
	s3 := newTestSession(c2, 3, "c", -1)
	r.add(s3)
	if r.find(c2) != s3 || r.byName("c") != s3 {
		t.Fatalf("二次登录后应命中新会话")
	}
	if r.byName("b") != nil {
		t.Fatalf("旧会话应从注册表摘除")
	}
	count := 0
	r.each(func(*Session) { count++ })
	if count != 1 {
		t.Fatalf("二次登录后应只剩 1 条会话，实际 %d", count)
	}
}
