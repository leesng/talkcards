// poison_recover_test.go: recovery on an unexpected error phase — roll back
// to the last good state, broadcast the notice, replay frames, continue.
package hub

import (
	"testing"

	"talkcards/backend/internal/card"
	"talkcards/backend/internal/game"
	"talkcards/backend/internal/table"
)

// TestPhaseErrorRecoversToLastGood: after injecting the error phase, the next
// play triggers recovery — state rolls back, everyone gets the replay frames,
// and the game continues.
func TestPhaseErrorRecoversToLastGood(t *testing.T) {
	h, conns, P, hands := startTurnRejectGame(t)
	W := (P + 1) % table.SeatCount
	first := hands[P][0]

	// Leader plays once to produce a last-good snapshot
	h.onPlayCard(conns[P], []card.Card{first})
	decodeFrame[playCardSuccess](t, waitFrame(t, conns[P], EvPlayCardOk, 1), EvPlayCardOk)
	waitFrame(t, conns[P], EvCtxPlayChange, 2)
	d := h.lobby.Desk(1)
	if d.LastGood == nil {
		t.Fatalf("有效出牌后应保存最近有效快照")
	}

	// Inject the error phase; the next player's play triggers recovery (discarded)
	d.Game.Poison()
	h.onPlayCard(conns[W], []card.Card{hands[W][0]})

	// The next player receives the recovery notice
	waitFrame(t, conns[W], EvMessage, 1)
	// Replayed GAME_START: hand back to the pre-play count (play was discarded)
	if n := len(hands[W]); handCount(t, h, 1, W) != n {
		t.Fatalf("恢复后手牌应为 %d 张", n)
	}
	// Rolled back: playing phase, turn still on W
	if d.Game.Phase() != game.PhasePlaying || d.Game.Turn() != W {
		t.Fatalf("恢复后应处于出牌阶段且轮到 %d，实际 phase=%d turn=%d", W, d.Game.Phase(), d.Game.Turn())
	}

	// Game continues: W beats "first" with the highest single and it succeeds
	top := hands[W][len(hands[W])-1]
	h.onPlayCard(conns[W], []card.Card{top})
	decodeFrame[playCardSuccess](t, waitFrame(t, conns[W], EvPlayCardOk, 1), EvPlayCardOk)
	next := decodeFrame[ctxPlayFrame](t, waitFrame(t, conns[W], EvCtxPlayChange, 4), "CTX")
	if next.PosID == W {
		t.Fatalf("恢复后出牌应正常轮转，实际仍指向 %d", next.PosID)
	}
}

// TestRecoverBeforeFirstPlay: error phase before the first lead — recovery
// rolls back to the opening snapshot without crashing or ending the game.
func TestRecoverBeforeFirstPlay(t *testing.T) {
	h, conns, P, _ := startTurnRejectGame(t)
	d := h.lobby.Desk(1)
	if d.LastGood == nil {
		t.Fatalf("开局即应有初始快照")
	}

	d.Game.Poison()
	h.onPlayCard(conns[P], []card.Card{})
	waitFrame(t, conns[P], EvMessage, 1)

	if d.Game.Phase() != game.PhasePlaying || d.Game.Turn() != P {
		t.Fatalf("恢复后应回到出牌阶段且轮转不变（%d），实际 phase=%d turn=%d",
			P, d.Game.Phase(), d.Game.Turn())
	}
	// The leader can still play normally after recovery
	h.onPlayCard(conns[P], []card.Card{d.Game.Hands()[idxOf(t, d, P)].Cards[0]})
	decodeFrame[playCardSuccess](t, waitFrame(t, conns[P], EvPlayCardOk, 1), EvPlayCardOk)
}

func idxOf(t *testing.T, d *table.Desk, pos int) int {
	t.Helper()
	for i, hs := range d.Game.Hands() {
		if hs.PosID == pos {
			return i
		}
	}
	t.Fatalf("座 %d 无手牌快照", pos)
	return 0
}
