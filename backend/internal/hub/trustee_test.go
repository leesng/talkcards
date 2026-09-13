// trustee_test.go 托管功能测试：对局中手动开关、托管中拒绝手动操作、
// 断线超时自动托管续局、全部真实玩家超时直接中止。
package hub

import (
	"path/filepath"
	"testing"
	"time"

	"talkcards/backend/internal/store"
)

// newTrusteeHub 建好测试 hub：1ms 机器人延迟、极短重连超时
func newTrusteeHub(t *testing.T) *Hub {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "trustee.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	h := New(st, 20*time.Millisecond, nil)
	h.botDelayMin, h.botDelayMax = time.Millisecond, time.Millisecond
	return h
}

// waitGameOver 轮询至对局结束（正常终局或终止复位后 Game 不在进行中）
func waitGameOver(t *testing.T, h *Hub, deskID int) {
	t.Helper()
	d := h.lobby.Desk(deskID)
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		h.mu.Lock()
		inProg := d.GameInProgress()
		h.mu.Unlock()
		if !inProg {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("60s 未终局")
}

// waitEventIn 等待 frames[from:] 中出现指定事件（pump 异步投递，需轮询）
func waitEventIn(t *testing.T, f *fakeConn, event string, from int) bool {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		for _, fr := range f.snapshot()[from:] {
			if ev, _, ok := cutFrame(fr); ok && ev == event {
				return true
			}
		}
		time.Sleep(time.Millisecond)
	}
	return false
}

// TestToggleTrusteeOnlyInGame 未开局时托管应被拒绝（只有对局中才能托管）
func TestToggleTrusteeOnlyInGame(t *testing.T) {
	h := newTrusteeHub(t)
	c0 := &fakeConn{id: "c0"}
	h.onLogin(c0, "host")
	h.onSitdown(c0, sitdownReq{DeskID: 1, PosID: 3})
	h.onPrepare(c0)

	before := len(c0.snapshot())
	h.onToggleTrustee(c0)
	if !waitEventIn(t, c0, EvMessage, before) {
		t.Fatalf("未开局托管应回 MESSAGE 拒绝，帧:\n%s", formatFrames(c0.snapshot()[before:]))
	}
	if seat := h.lobby.Desk(1).Seat(3); seat.Trustee {
		t.Fatalf("未开局托管不应生效")
	}
}

// TestTrusteeManualFullGame 对局中手动托管：机器人代打整局直至终局，
// 托管中手动操作被拒，终局后托管标记清除
func TestTrusteeManualFullGame(t *testing.T) {
	h := newTrusteeHub(t)
	c0 := &fakeConn{id: "c0"}
	h.onLogin(c0, "host")
	h.onSitdown(c0, sitdownReq{DeskID: 1, PosID: 3})
	h.onPrepare(c0)
	h.onHostStartGame(c0, hostStartGameReq{FillBots: true})
	waitFrame(t, c0, EvGameStart, 1)

	d := h.lobby.Desk(1)
	if seat := d.Seat(3); seat.Trustee {
		t.Fatalf("开局不应自动托管")
	}

	// 叫分阶段先托管：系统代打叫分
	before := len(c0.snapshot())
	h.onToggleTrustee(c0)
	if !d.Seat(3).Trustee {
		t.Fatalf("对局中托管应生效")
	}
	if !waitEventIn(t, c0, EvTrusteeChange, before) {
		t.Fatalf("应广播 TRUSTEE_CHANGE")
	}

	// 托管中手动出牌被拒（MESSAGE，不进牌局状态机；pump 异步投递需轮询）
	before2 := len(c0.snapshot())
	h.onPlayCard(c0, nil)
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		for _, fr := range c0.snapshot()[before2:] {
			if ev, _, ok := cutFrame(fr); ok && ev == EvMessage {
				goto rejected
			}
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("托管中手动出牌应被拒，帧:\n%s", formatFrames(c0.snapshot()[before2:]))
rejected:

	waitGameOver(t, h, 1)

	if seat := d.Seat(3); seat.Trustee {
		t.Fatalf("终局后托管标记应清除")
	}
	if !waitEventIn(t, c0, EvGameOver, 0) {
		t.Fatalf("托管整局应由系统代打至正常终局")
	}
}

// TestReconnectTimeoutTrusteeContinues 断线超时自动托管：
// 还有别的真人在线 → 对局继续（不终止）；全部真实玩家超时 → 直接中止
func TestReconnectTimeoutTrusteeContinues(t *testing.T) {
	h := newTrusteeHub(t)

	// 两个真人（座 3、座 5），其余机器人
	c0 := &fakeConn{id: "c0"}
	h.onLogin(c0, "host")
	h.onSitdown(c0, sitdownReq{DeskID: 1, PosID: 3})
	h.onPrepare(c0)
	c1 := &fakeConn{id: "c1"}
	h.onLogin(c1, "guest")
	h.onSitdown(c1, sitdownReq{DeskID: 1, PosID: 5})
	h.onPrepare(c1)
	h.onHostStartGame(c0, hostStartGameReq{FillBots: true})
	waitFrame(t, c0, EvGameStart, 1)

	d := h.lobby.Desk(1)

	// guest 掉线：座位保留 + 重连倒计时
	h.onDisconnect(c1)
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		h.mu.Lock()
		held := func() bool { _, ok := d.Hold("guest"); return ok }()
		inProg := d.GameInProgress()
		h.mu.Unlock()
		if held && !inProg {
			break // 超时触发后：无真人在线 → 已终止
		}
		time.Sleep(time.Millisecond)
	}

	// guest 超时后：仅剩 host 一个真人在线 → 转托管、对局继续
	h.mu.Lock()
	inProg := d.GameInProgress()
	trustee := d.Seat(5).Trustee
	held := func() bool { _, ok := d.Hold("guest"); return ok }()
	h.mu.Unlock()
	if !inProg {
		t.Fatalf("尚有真人在线，超时应转托管续局而非终止")
	}
	if !trustee {
		t.Fatalf("超时玩家座位应被自动托管")
	}
	if !held {
		t.Fatalf("超时托管后应保留重连记录（可随时重连接管）")
	}

	// host 也掉线超时：全部真实玩家不在 → 对局直接中止
	h.onDisconnect(c0)
	waitGameOver(t, h, 1)
}

// TestTrusteeReconnectCancel 断线超时托管后重连：自动取消托管、恢复手动
func TestTrusteeReconnectCancel(t *testing.T) {
	h := newTrusteeHub(t)

	c0 := &fakeConn{id: "c0"}
	h.onLogin(c0, "host")
	h.onSitdown(c0, sitdownReq{DeskID: 1, PosID: 3})
	h.onPrepare(c0)
	c1 := &fakeConn{id: "c1"}
	h.onLogin(c1, "guest")
	h.onSitdown(c1, sitdownReq{DeskID: 1, PosID: 5})
	h.onPrepare(c1)
	h.onHostStartGame(c0, hostStartGameReq{FillBots: true})
	waitFrame(t, c0, EvGameStart, 1)

	d := h.lobby.Desk(1)
	h.onDisconnect(c1)

	// 等 guest 超时转托管
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		h.mu.Lock()
		trustee := d.Seat(5).Trustee
		h.mu.Unlock()
		if trustee {
			break
		}
		time.Sleep(time.Millisecond)
	}
	h.mu.Lock()
	if !d.Seat(5).Trustee {
		h.mu.Unlock()
		t.Fatalf("guest 超时应自动托管")
	}
	h.mu.Unlock()

	// guest 重连：坐回原座、托管取消
	c2 := &fakeConn{id: "c2"}
	h.onLogin(c2, "guest")
	waitFrame(t, c2, EvReconnect, 1)
	h.mu.Lock()
	trustee := d.Seat(5).Trustee
	held := func() bool { _, ok := d.Hold("guest"); return ok }()
	inProg := d.GameInProgress()
	h.mu.Unlock()
	if trustee {
		t.Fatalf("重连后应取消托管")
	}
	if held {
		t.Fatalf("重连后应清除保留记录")
	}
	if !inProg {
		t.Fatalf("重连后对局应仍在进行（另一个真人在线，超时未终止）")
	}
}
