// Reconnect replay regression tests.
package hub

import (
	"path/filepath"
	"testing"
	"time"

	"talkcards/backend/internal/card"
	"talkcards/backend/internal/game"
	"talkcards/backend/internal/store"
	"talkcards/backend/internal/table"
)

// TestReconnectReplaysStandingPlay: after a pass, the standing play is resent.
func TestReconnectReplaysStandingPlay(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "replay.db"))
	if err != nil {
		t.Fatalf("打开测试库失败: %v", err)
	}
	defer st.Close()
	h := New(st, 0, nil)

	conns := make([]*fakeConn, table.SeatCount)
	for i := 0; i < table.SeatCount; i++ {
		conns[i] = &fakeConn{id: "c" + string(rune('0'+i))}
		h.onLogin(conns[i], "u"+string(rune('0'+i)))
		h.onSitdown(conns[i], sitdownReq{DeskID: 1, PosID: i})
	}
	for i := 0; i < table.SeatCount; i++ {
		h.onPrepare(conns[i])
	}
	h.onHostStartGame(conns[0], hostStartGameReq{})

	d := h.lobby.Desk(1)
	g := d.Game
	if g == nil {
		t.Fatal("开局后对局应挂载到桌面")
	}

	leader := g.Turn()
	waitFrame(t, conns[0], EvShowTopCard, 1)

	var lead card.Card
	for _, hs := range g.Hands() {
		if hs.PosID == leader && len(hs.Cards) > 0 {
			lead = hs.Cards[0]
		}
	}
	if lead == (card.Card{}) {
		t.Fatal("首出者应持有手牌")
	}
	h.onPlayCard(conns[leader], []card.Card{lead})

	// Next player passes: LastPlay is the pass; the standing play is still the lead.
	passer := g.Turn()
	if passer == leader {
		t.Fatal("首出后轮转不应仍在首出者")
	}
	h.onPlayCard(conns[passer], []card.Card{})

	if d.LastPlay == nil || !d.LastPlay.IsPass {
		t.Fatalf("最近一次动作应为过牌，实际 %+v", d.LastPlay)
	}
	if d.LastValidPlay == nil || len(d.LastValidPlay.Cards) != 1 || d.LastValidPlay.Cards[0] != lead {
		t.Fatalf("桌面有效牌快照应保留首出单张，实际 %+v", d.LastValidPlay)
	}

	// Bystander seat (neither leader nor passer) disconnects.
	victim := -1
	for i := 0; i < table.SeatCount; i++ {
		if i != leader && i != passer && i != g.Turn() {
			victim = i
			break
		}
	}
	if victim < 0 {
		t.Fatal("找不到可用于断线的旁观座位")
	}
	victimName := "u" + string(rune('0'+victim))
	h.onDisconnect(conns[victim])

	// Reconnect: standing play frame first, then the latest pass.
	back := &fakeConn{id: "back"}
	h.onLogin(back, victimName)
	waitFrame(t, back, EvReconnect, 1)
	waitFrame(t, back, EvGameStart, 1)
	waitFrame(t, back, EvShowTopCard, 1)

	first := decodeFrame[ctxPlayChange](t, waitFrame(t, back, EvCtxPlayChange, 1), EvCtxPlayChange)
	if !first.Replay {
		t.Fatalf("重连重放帧应带 replay 标记: %+v", first)
	}
	if first.IsPass {
		t.Fatalf("第 1 帧应是桌面有效牌而非过牌: %+v", first)
	}
	if first.CtxData.PosID != leader || len(first.CtxData.Cards) != 1 || first.CtxData.Cards[0] != lead {
		t.Fatalf("第 1 帧应还原首出者的牌（pos=%d card=%+v），实际 %+v", leader, lead, first.CtxData)
	}
	if first.CtxData.Key == "" || first.CtxData.Type == "" {
		t.Fatalf("桌面有效牌应带牌型信息（key/type）: %+v", first.CtxData)
	}

	second := decodeFrame[ctxPlayChange](t, waitFrame(t, back, EvCtxPlayChange, 2), EvCtxPlayChange)
	if !second.Replay {
		t.Fatalf("重连重放帧应带 replay 标记: %+v", second)
	}
	if !second.IsPass || second.CtxData.PosID != passer {
		t.Fatalf("第 2 帧应是过牌者的动作（pos=%d），实际 %+v", passer, second)
	}
	if second.PosID != g.Turn() {
		t.Fatalf("重放帧的轮转应指向当前出牌方 %d，实际 %d", g.Turn(), second.PosID)
	}
}

// TestTrickClearFlag: only the final pass of a collected trick carries clear.
func TestTrickClearFlag(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "clear.db"))
	if err != nil {
		t.Fatalf("打开测试库失败: %v", err)
	}
	defer st.Close()
	h := New(st, 0, nil)

	conns := make([]*fakeConn, table.SeatCount)
	for i := 0; i < table.SeatCount; i++ {
		conns[i] = &fakeConn{id: "k" + string(rune('0'+i))}
		h.onLogin(conns[i], "v"+string(rune('0'+i)))
		h.onSitdown(conns[i], sitdownReq{DeskID: 1, PosID: i})
	}
	for i := 0; i < table.SeatCount; i++ {
		h.onPrepare(conns[i])
	}
	h.onHostStartGame(conns[0], hostStartGameReq{})

	d := h.lobby.Desk(1)
	g := d.Game
	leader := g.Turn()
	waitFrame(t, conns[0], EvShowTopCard, 1)

	var lead card.Card
	for _, hs := range g.Hands() {
		if hs.PosID == leader && len(hs.Cards) > 0 {
			lead = hs.Cards[0]
		}
	}
	h.onPlayCard(conns[leader], []card.Card{lead})

	playFrameNo := 2 // #1 is the lead frame
	pf := decodeFrame[ctxPlayChange](t, waitFrame(t, conns[0], EvCtxPlayChange, playFrameNo), EvCtxPlayChange)
	if pf.Clear {
		t.Fatalf("有效出牌帧不应带 clear（圈仍进行中）: %+v", pf)
	}

	// Everyone else passes until rotation returns to the leader.
	passes := 0
	for g.Turn() != leader {
		passer := g.Turn()
		h.onPlayCard(conns[passer], nil)
		passes++
		if passes > table.SeatCount {
			t.Fatal("过牌轮转异常，未能回到首出者")
		}
	}
	if passes == 0 {
		t.Fatal("应至少有一次过牌才构成一圈收分")
	}

	// Last pass carries clear; intermediate ones don't.
	for i := 1; i <= passes; i++ {
		fr := decodeFrame[ctxPlayChange](t, waitFrame(t, conns[0], EvCtxPlayChange, playFrameNo+i), EvCtxPlayChange)
		last := i == passes
		if fr.Clear != last {
			t.Fatalf("第 %d 次过牌帧 clear=%v，期望 %v", i, fr.Clear, last)
		}
		if last && fr.PosID != leader {
			t.Fatalf("收分后轮转应回到首出者 %d 领出新圈，实际 %d", leader, fr.PosID)
		}
	}
}

// TestBotPlayFrameCarriesRealSeat: regression — ctxData.posId must be the
// real seat; a past bug defaulted it to 0, crediting all bot plays to seat 0.
func TestBotPlayFrameCarriesRealSeat(t *testing.T) {
	st, _ := store.Open(filepath.Join(t.TempDir(), "botseat.db"))
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
	for _, fr := range c0.snapshot() {
		if ev, data, ok := cutFrame(fr); ok && ev == EvGameStart {
			gs := decodeFrame[gameStartPayload](t, data, EvGameStart)
			humanHand = append([]card.Card(nil), gs.Cards[3].Cards...)
		}
	}

	// trySingle: emit a single, poll for the response frame (async pump).
	// All rejected → pass; but passing is illegal when leading, so passing
	// throughout would stall rotation on your own lead.
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

	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		if _, ok := findEvent(c0, EvGameOver); ok {
			break
		}
		h.mu.Lock()
		phase, turn := d.Game.Phase(), d.Game.Turn()
		h.mu.Unlock()
		if phase == game.PhasePlaying && turn == 3 {
			played := false
			for i := 0; i < len(humanHand) && !played; i++ {
				if trySingle(humanHand[i]) {
					humanHand = append(humanHand[:i], humanHand[i+1:]...)
					played = true
				}
			}
			if !played {
				h.onPlayCard(c0, nil) // cannot beat: pass
			}
		} else {
			time.Sleep(2 * time.Millisecond)
		}
	}
	if _, ok := findEvent(c0, EvGameOver); !ok {
		t.Fatal("60 秒内未终局")
	}

	// Seat distribution across bot play frames (posId != 3).
	botSeats := map[int]int{}
	for _, fr := range c0.snapshot() {
		ev, data, ok := cutFrame(fr)
		if !ok || ev != EvCtxPlayChange {
			continue
		}
		pc := decodeFrame[ctxPlayChange](t, data, EvCtxPlayChange)
		if pc.CtxData.PosID != 3 && pc.CtxData.Len > 0 {
			botSeats[pc.CtxData.PosID]++
		}
	}
	if len(botSeats) < 3 {
		t.Fatalf("机器人有效出牌帧座位分布过窄 %v（修复前全部错记为 0 号座位）", botSeats)
	}
}
