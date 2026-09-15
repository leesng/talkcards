// spectate_test.go: 观战模式回归 — 中途加入、脱敏 GAME_START、帧持续到达、
// 操作被拒、退出与断线不影响对局。
package hub

import (
	"path/filepath"
	"testing"
	"time"

	"talkcards/backend/internal/game"
	"talkcards/backend/internal/store"
	"talkcards/backend/internal/table"
)

// startBotsDesk: fill desk 1 with bots and start; returns the desk.
func startBotsDesk(t *testing.T, h *Hub) *table.Desk {
	t.Helper()
	d := h.lobby.Desk(1)
	d.FillBots()
	h.startGame(1)
	if !d.GameInProgress() {
		t.Fatalf("开局后应对局进行中")
	}
	return d
}

// spectateJoin: log in a fresh spectator connection on an in-progress desk.
func spectateJoin(t *testing.T, h *Hub, name string, deskID int) *fakeConn {
	t.Helper()
	c := &fakeConn{id: name}
	h.onLogin(c, name)
	h.onSpectate(c, sitdownReq{DeskID: deskID})
	waitFrame(t, c, EvSpectateSuccess, 1)
	return c
}

// TestSpectateMidGame: bot game on desk 1, spectator joins mid-game and sees
// everything except hand faces; GAME_START is redacted to sizes.
func TestSpectateMidGame(t *testing.T) {
	st, _ := store.Open(filepath.Join(t.TempDir(), "spec.db"))
	defer st.Close()
	h := New(st, 0, nil)
	h.botDelayMin, h.botDelayMax = time.Millisecond, time.Millisecond

	d := startBotsDesk(t, h)

	// Wait for the first accepted play so the join is truly mid-game.
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		h.mu.Lock()
		_, hasTop := d.Game.TrickTop()
		h.mu.Unlock()
		if hasTop {
			break
		}
		time.Sleep(time.Millisecond)
	}

	// Freeze the desk's bot timer so the join-time snapshot is stable to verify.
	h.mu.Lock()
	if t := h.botTimers[1]; t != nil {
		t.Stop()
		delete(h.botTimers, 1)
	}
	h.mu.Unlock()

	sp := spectateJoin(t, h, "watcher", 1)

	// Redacted GAME_START: every hand empty, count == live hand length.
	raw, ok := findEvent(sp, EvGameStart)
	if !ok {
		t.Fatalf("观战者应收到 GAME_START")
	}
	gs := decodeFrame[gameStartPayload](t, raw, EvGameStart)
	h.mu.Lock()
	lens := map[int]int{}
	for _, hs := range d.Game.Hands() {
		lens[hs.PosID] = len(hs.Cards)
	}
	h.mu.Unlock()
	for _, g := range gs.Cards {
		if len(g.Cards) != 0 {
			t.Fatalf("观战 GAME_START 泄漏手牌: pos=%d cards=%v", g.ID, g.Cards)
		}
		if g.Count != lens[g.ID] {
			t.Fatalf("观战张数不符: pos=%d count=%d 实际=%d", g.ID, g.Count, lens[g.ID])
		}
	}
	if _, ok := findEvent(sp, EvShowTopCard); !ok {
		t.Fatalf("观战者应收到 SHOW_TOP_CARD")
	}
	if n := countEvent(sp, EvCtxPlayChange); n == 0 {
		t.Fatalf("观战者应收到 CTX_PLAY_CHANGE 重放帧")
	}

	// Unfreeze the bots and let the game run out while being watched.
	h.mu.Lock()
	h.scheduleBotIfTurn(d)
	h.mu.Unlock()

	// Live frames keep arriving until GAME_OVER; PLAY_CARD_SUCCESS must never
	// reach the spectator.
	deadline = time.Now().Add(60 * time.Second)
	ctxSeen := countEvent(sp, EvCtxPlayChange)
	for time.Now().Before(deadline) {
		if _, ok := findEvent(sp, EvGameOver); ok {
			return // full game observed
		}
		h.mu.Lock()
		phase := d.Game.Phase()
		h.mu.Unlock()
		if phase == game.PhaseOver {
			deadline = time.Now().Add(2 * time.Second) // frame in flight
		}
		if n := countEvent(sp, EvCtxPlayChange); n > ctxSeen {
			ctxSeen = n
		} else if phase == game.PhasePlaying {
			// stalled? give the desk a nudge window before failing below
		}
		if _, ok := findEvent(sp, EvPlayCardOk); ok {
			t.Fatalf("观战者不应收到 PLAY_CARD_SUCCESS")
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("观战者未在对局结束前持续收到帧（GAME_OVER 未达）")
}

// TestSpectateGuards: spectator inputs are rejected; seated users cannot
// spectate; SITDOWN while spectating is refused.
func TestSpectateGuards(t *testing.T) {
	st, _ := store.Open(filepath.Join(t.TempDir(), "spec2.db"))
	defer st.Close()
	h := New(st, 0, nil)
	h.botDelayMin, h.botDelayMax = time.Millisecond, time.Millisecond

	startBotsDesk(t, h)

	sp := spectateJoin(t, h, "watcher", 1)

	// A seated user cannot spectate.
	p := &fakeConn{id: "player"}
	h.onLogin(p, "player")
	h.onSitdown(p, sitdownReq{DeskID: 2, PosID: 0})
	waitFrame(t, p, EvSitdownSuccess, 1)
	h.onSpectate(p, sitdownReq{DeskID: 1})
	if _, ok := findEvent(p, EvSpectateSuccess); ok {
		t.Fatalf("入座用户不应能观战")
	}

	// Spectator plays/prepare/chat must be ignored and never touch the game.
	h.mu.Lock()
	phaseBefore := h.lobby.Desk(1).Game.Phase()
	h.mu.Unlock()
	h.onPlayCard(sp, nil)
	h.onPrepare(sp)
	h.onUserMessage(sp, "你好")
	h.mu.Lock()
	phaseAfter := h.lobby.Desk(1).Game.Phase()
	h.mu.Unlock()
	if phaseBefore != phaseAfter {
		t.Fatalf("观战者操作改变了对局状态: %v -> %v", phaseBefore, phaseAfter)
	}
	for _, fr := range sp.snapshot() {
		if ev, _, ok := cutFrame(fr); ok && ev == EvPrepareSuccess {
			t.Fatalf("观战者不应收到 PREPARE_SUCCESS")
		}
	}

	// SITDOWN while spectating is refused.
	h.onSitdown(sp, sitdownReq{DeskID: 2, PosID: 0})
	if _, ok := findEvent(sp, EvSitdownSuccess); ok {
		t.Fatalf("观战中不应能入座")
	}

	// Spectating an idle desk fails.
	idle := &fakeConn{id: "idle"}
	h.onLogin(idle, "idle")
	h.onSpectate(idle, sitdownReq{DeskID: 2})
	waitFrame(t, idle, EvSpectateError, 1)
}

// TestSpectateLeaveAndDisconnect: UNSITDOWN exits spectating; a spectator
// disconnect never terminates the game.
func TestSpectateLeaveAndDisconnect(t *testing.T) {
	st, _ := store.Open(filepath.Join(t.TempDir(), "spec3.db"))
	defer st.Close()
	h := New(st, 0, nil)
	h.botDelayMin, h.botDelayMax = time.Millisecond, time.Millisecond

	d := startBotsDesk(t, h)

	sp := spectateJoin(t, h, "watcher", 1)
	h.onUnsitdown(sp)
	waitFrame(t, sp, EvUnsitSuccess, 1)
	h.mu.Lock()
	s := h.sessions.find(sp)
	desk := -1
	if s != nil {
		desk = s.deskID
	}
	h.mu.Unlock()
	if desk != -1 {
		t.Fatalf("退出观战后会话应回大厅")
	}
	if !d.GameInProgress() {
		t.Fatalf("退出观战不应影响对局")
	}

	// Disconnect path: spectator drops, game keeps running.
	sp2 := spectateJoin(t, h, "watcher2", 1)
	h.onDisconnect(sp2)
	h.mu.Lock()
	still := d.GameInProgress()
	h.mu.Unlock()
	if !still {
		t.Fatalf("观战者断线不应终止对局")
	}

	// Game runs to completion unaffected.
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		h.mu.Lock()
		phase := d.Game.Phase()
		h.mu.Unlock()
		if phase == game.PhaseOver || phase == game.PhaseIdle {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("60s 未终局")
}

// TestDeskStateBroadcast: lobby clients get deskState 2 on start and 0 on end.
func TestDeskStateBroadcast(t *testing.T) {
	st, _ := store.Open(filepath.Join(t.TempDir(), "spec4.db"))
	defer st.Close()
	h := New(st, 0, nil)
	h.botDelayMin, h.botDelayMax = time.Millisecond, time.Millisecond

	lobby := &fakeConn{id: "lobby"}
	h.onLogin(lobby, "lobbyman")

	d := startBotsDesk(t, h)

	wantState := func(want int) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			for _, fr := range lobby.snapshot() {
				if ev, data, ok := cutFrame(fr); ok && ev == EvStatusChange {
					sc := decodeFrame[houseStatusChange](t, data, EvStatusChange)
					if sc.DeskState != nil && *sc.DeskState == want && sc.PosID == -1 {
						return
					}
				}
			}
			time.Sleep(time.Millisecond)
		}
		t.Fatalf("大厅未收到 deskState=%d 的 STATUS_CHANGE", want)
	}
	wantState(2)

	// Force termination (escape path) and expect deskState 0.
	h.mu.Lock()
	h.terminateGame(d, 0, "逃")
	h.mu.Unlock()
	wantState(0)
	h.mu.Lock()
	idle := d.State == 0
	h.mu.Unlock()
	if !idle {
		t.Fatalf("终局后桌状态应复位为 0")
	}
}
