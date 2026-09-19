// turn_reject_test.go: out-of-turn no longer poisons the game — rejected with
// an error + re-pushed turn frame, and the game continues. Mirrors the three
// trigger paths in scripts/poison-repro.js (S1 play / S2 second play / S3 pass).
package hub

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"reflect"
	"testing"

	"talkcards/backend/internal/card"
	"talkcards/backend/internal/store"
	"talkcards/backend/internal/table"
)

// startTurnRejectGame: start a game with 8 real players; returns conns, leader P and hands.
func startTurnRejectGame(t *testing.T) (*Hub, []*fakeConn, int, [][]card.Card) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "turn-reject.db"))
	if err != nil {
		t.Fatalf("打开测试库失败: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	h := New(st, 0, nil)

	conns := make([]*fakeConn, table.SeatCount)
	for i := 0; i < table.SeatCount; i++ {
		conns[i] = &fakeConn{id: fmt.Sprintf("c%d", i)}
		h.onLogin(conns[i], fmt.Sprintf("u%d", i))
		h.onSitdown(conns[i], sitdownReq{DeskID: 1, PosID: i})
		h.onPrepare(conns[i])
	}
	h.onHostStartGame(conns[0], hostStartGameReq{})

	var start struct {
		Cards []struct {
			Cards []card.Card `json:"cards"`
		} `json:"cards"`
	}
	if err := json.Unmarshal([]byte(waitFrame(t, conns[0], EvGameStart, 1)), &start); err != nil {
		t.Fatalf("GAME_START 解码失败: %v", err)
	}
	hands := make([][]card.Card, table.SeatCount)
	for i, grp := range start.Cards {
		hands[i] = grp.Cards
	}
	P := decodeFrame[showTopCard](t, waitFrame(t, conns[0], EvShowTopCard, 1), EvShowTopCard).DizhuPosID
	waitFrame(t, conns[0], EvCtxPlayChange, 1) // first lead turn frame
	return h, conns, P, hands
}

// TestOutOfTurnRejectedAndResync: out-of-turn play/pass → array error + turn
// resync frame; the game never enters the error phase and the leader still plays.
func TestOutOfTurnRejectedAndResync(t *testing.T) {
	h, conns, P, hands := startTurnRejectGame(t)
	W := (P + 1) % table.SeatCount // not the turn holder
	stale := hands[W][0]

	// S1/S2 shape: play a card in someone else's turn (legality is irrelevant
	// — the turn gate comes first).
	h.onPlayCard(conns[W], []card.Card{stale})
	if got, want := waitFrame(t, conns[W], EvPlayCardErr, 1), mustJSON(t, []card.Card{stale}); got != want {
		t.Fatalf("越序出牌应数组回错 %s，实际 %s", want, got)
	}
	// Resync frame: no cached frame before the first lead → leadFrame (Clear); posId still P.
	resync := decodeFrame[ctxPlayFrame](t, waitFrame(t, conns[W], EvCtxPlayChange, 2), "重同步 CTX")
	if resync.PosID != P || !resync.Clear {
		t.Fatalf("越序后应重推轮转帧（posId=%d, clear），实际 %+v", P, resync)
	}

	// S3 shape: out-of-turn pass is rejected too (empty hand no longer skips the turn gate).
	h.onPlayCard(conns[W], []card.Card{})
	if got, want := waitFrame(t, conns[W], EvPlayCardErr, 2), mustJSON(t, []card.Card{}); got != want {
		t.Fatalf("越序过牌应数组回错 %s，实际 %s", want, got)
	}
	waitFrame(t, conns[W], EvCtxPlayChange, 3) // second resync frame

	// Game alive: leader plays, gets SUCCESS, rotation advances.
	first := hands[P][0]
	h.onPlayCard(conns[P], []card.Card{first})
	ok := decodeFrame[playCardSuccess](t, waitFrame(t, conns[P], EvPlayCardOk, 1), EvPlayCardOk)
	if !reflect.DeepEqual(ok.Data, []card.Card{first}) {
		t.Fatalf("先手出牌 SUCCESS = %+v", ok)
	}
	next := decodeFrame[ctxPlayFrame](t, waitFrame(t, conns[P], EvCtxPlayChange, 2), "CTX")
	if next.PosID != W {
		t.Fatalf("出牌后应轮到 %d，实际 %d", W, next.PosID)
	}
}

// TestSecondPlayAfterOwnRejected: S2 — after a successful play, immediately
// play another card; the rotation has moved on so it is rejected as
// out-of-turn and the game continues (previously: poison deadlock).
func TestSecondPlayAfterOwnRejected(t *testing.T) {
	h, conns, P, hands := startTurnRejectGame(t)
	first, second := hands[P][0], hands[P][1]

	h.onPlayCard(conns[P], []card.Card{first})
	decodeFrame[playCardSuccess](t, waitFrame(t, conns[P], EvPlayCardOk, 1), EvPlayCardOk)

	h.onPlayCard(conns[P], []card.Card{second}) // second play
	if got, want := waitFrame(t, conns[P], EvPlayCardErr, 1), mustJSON(t, []card.Card{second}); got != want {
		t.Fatalf("二次出手应数组回错 %s，实际 %s", want, got)
	}
	resync := decodeFrame[ctxPlayFrame](t, waitFrame(t, conns[P], EvCtxPlayChange, 3), "重同步 CTX")
	if resync.PosID != (P+1)%table.SeatCount {
		t.Fatalf("二次出手后重同步帧应指向下一家，实际 %+v", resync.PosID)
	}
	// The second card was not deducted: still 41/40-1 cards.
	if n := len(hands[P]) - 1; handCount(t, h, 1, P) != n {
		t.Fatalf("二次出手被拒后手牌应为 %d 张", n)
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal 失败: %v", err)
	}
	return string(b)
}

func handCount(t *testing.T, h *Hub, deskID, pos int) int {
	t.Helper()
	d := h.lobby.Desk(deskID)
	if d == nil || d.Game == nil {
		t.Fatalf("桌 %d 无对局", deskID)
	}
	for _, hs := range d.Game.Hands() {
		if hs.PosID == pos {
			return len(hs.Cards)
		}
	}
	t.Fatalf("座 %d 无手牌快照", pos)
	return 0
}
