package hub

import (
	"path/filepath"
	"testing"
	"time"

	"talkcards/backend/internal/card"
	"talkcards/backend/internal/game"
	"talkcards/backend/internal/store"
)

// TestBotOnlyGameSelfPlay 8 个机器人自打整局，验证轮转/终局/清理无停滞
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
			// 终局后立即 ResetGame，轮询往往只看到 Idle：全部手牌已空即视为打完
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

// TestHumanWithBotsFullGame 7 机器人 + 座 3 真人打完整局：真人复刻 e2e 驱动策略
// （乱序逐张试单张、全被拒则过牌）。关键点：出牌后必须轮询等待 PLAY_CARD_SUCCESS/
// PLAY_CARD_ERROR 帧——pump 异步投递，同步读会误判被拒而重出同一张，触发越序毒化。
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

	// trySingle 与 e2e 驱动一致：emit 单张后查响应帧，SUCCESS 则出手牌，ERROR 换下一张
	// trySingle 与 e2e 驱动一致：emit 单张后轮询等 SUCCESS/ERROR 帧（pump 异步投递，
	// 同步读 snapshot 会误判被拒→重出同一张→越序毒化），SUCCESS 则出手牌，ERROR 换下一张
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
			return // 正常终局
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
				h.onPlayCard(c0, nil) // 全被拒则过牌
			}
		default:
			// 出现新的 GAME_START（重发）时才刷新手牌，避免每拍重置丢失已出牌
			if n := countEvent(c0, EvGameStart); n != lastGameStart {
				lastGameStart = n
				loadHand()
			}
			time.Sleep(time.Millisecond)
		}
	}
	t.Fatalf("60s 未终局（可能停滞）turn=%d", lastTurn)
}
