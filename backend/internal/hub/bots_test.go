package hub

import (
	"path/filepath"
	"testing"
	"time"

	"talkcards/backend/internal/card"
	"talkcards/backend/internal/game"
	"talkcards/backend/internal/store"
)

// TestBotOnlyGameSelfPlay: 8 bots self-play a full game without stalling.
func TestBotOnlyGameSelfPlay(t *testing.T) {
	st, _ := store.Open(filepath.Join(t.TempDir(), "diag.db"))
	defer st.Close()
	h := New(st, 0, nil)
	h.botDelayMin, h.botDelayMax = time.Millisecond, time.Millisecond

	d := h.lobby.Desk(1)
	d.FillBots()
	h.startGame(1)

	deadline := time.Now().Add(60 * time.Second)
	lastTurn, lastPhase, lastChange := -99, game.Phase(-1), time.Now()
	for time.Now().Before(deadline) {
		h.mu.Lock()
		g := d.Game
		phase, turn, trickPos := g.Phase(), g.Turn(), g.TrickPos()
		lens := g.Hands()
		h.mu.Unlock()

		if phase == game.PhaseOver {
			t.Logf("正常终局: winner=%v", g.Result().Winner)
			return
		}
		if phase == game.PhaseIdle {
			// ResetGame runs right after the finish; all-empty hands = done.
			empty := true
			for _, hs := range lens {
				if len(hs.Cards) > 0 {
					empty = false
					break
				}
			}
			if empty {
				t.Logf("正常终局（重置后）")
				return
			}
		}
		if turn != lastTurn || phase != lastPhase {
			lastTurn, lastPhase, lastChange = turn, phase, time.Now()
		} else if time.Since(lastChange) > 3*time.Second {
			h.mu.Lock()
			top, ok := g.TrickTop()
			handStr := ""
			for _, hs := range lens {
				handStr += " " + itoa(hs.PosID) + ":" + itoa(len(hs.Cards))
			}
			h.mu.Unlock()
			t.Fatalf("停滞: phase=%v turn=%d trickPos=%d top=%v ok=%v hands:%s",
				phase, turn, trickPos, top, ok, handStr)
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("60s 未终局")
}

// TestHumanWithBotsFullGame: 7 bots + a human at seat 3. Pitfall: frames
// arrive async via the pump — after each play you MUST poll for the
// SUCCESS/ERROR frame; a synchronous read misjudges it as rejected, replays
// the same card, and poisons the game out of turn.
func TestHumanWithBotsFullGame(t *testing.T) {
	st, _ := store.Open(filepath.Join(t.TempDir(), "diag2.db"))
	defer st.Close()
	h := New(st, 0, nil)
	h.botDelayMin, h.botDelayMax = time.Millisecond, time.Millisecond

	c0 := &fakeConn{id: "c0"}
	h.onLogin(c0, "host")
	h.onSitdown(c0, sitdownReq{DeskID: 1, PosID: 3})
	h.onPrepare(c0)
	h.onHostStartGame(c0, hostStartGameReq{FillBots: true})
	waitFrame(t, c0, EvGameStart, 1)

	d := h.lobby.Desk(1)
	var humanHand []card.Card

	loadHand := func() {
		for _, fr := range c0.snapshot() {
			if ev, data, ok := cutFrame(fr); ok && ev == EvGameStart {
				gs := decodeFrame[gameStartPayload](t, data, EvGameStart)
				humanHand = append([]card.Card(nil), gs.Cards[3].Cards...)
			}
		}
	}

	// trySingle: emit a single, poll for the response frame (see pitfall above).
	trySingle := func(c card.Card) bool {
		before := len(c0.snapshot())
		h.onPlayCard(c0, []card.Card{c})
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			for _, fr := range c0.snapshot()[before:] {
				ev, _, cut := cutFrame(fr)
				if !cut {
					continue
				}
				switch ev {
				case EvPlayCardOk:
					return true
				case EvPlayCardErr:
					return false
				}
			}
			time.Sleep(time.Millisecond)
		}
		t.Fatalf("真人试单张 %v 等待响应帧超时", c)
		return false
	}

	loadHand()
	lastGameStart := countEvent(c0, EvGameStart)
	lastTurn, lastChange := -99, time.Now()
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		h.mu.Lock()
		phase, turn := d.Game.Phase(), d.Game.Turn()
		h.mu.Unlock()

		if _, ok := findEvent(c0, EvGameOver); ok {
			return // finished normally
		}
		if turn != lastTurn {
			lastTurn, lastChange = turn, time.Now()
		} else if time.Since(lastChange) > 3*time.Second {
			h.mu.Lock()
			top, ok := d.Game.TrickTop()
			trickPos := d.Game.TrickPos()
			h.mu.Unlock()
			t.Fatalf("停滞: phase=%v turn=%d trickPos=%d top=%v ok=%v 真人手牌%d张",
				phase, turn, trickPos, top, ok, len(humanHand))
		}

		switch {
		case phase == game.PhasePlaying && turn == 3:
			played := false
			for i := 0; i < len(humanHand) && !played; i++ {
				if trySingle(humanHand[i]) {
					humanHand = append(humanHand[:i], humanHand[i+1:]...)
					played = true
				}
			}
			if !played {
				h.onPlayCard(c0, nil) // every single rejected: pass
			}
		default:
			// Reload the hand only on a new GAME_START, else played cards are lost.
			if n := countEvent(c0, EvGameStart); n != lastGameStart {
				lastGameStart = n
				loadHand()
			}
			time.Sleep(time.Millisecond)
		}
	}
	t.Fatalf("60s 未终局（可能停滞）turn=%d", lastTurn)
}
