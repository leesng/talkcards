// translator.go: domain state → wire payloads; rebuilds JS numeric-key objects.
package hub

import (
	"talkcards/backend/internal/card"
	"talkcards/backend/internal/game"
	"talkcards/backend/internal/table"
)

func newGameStart(g *game.Game) gameStartPayload {
	hands := g.Hands()
	groups := make([]handGroup, len(hands))
	for _, h := range hands {
		ht := [15]int{}
		for _, c := range h.Cards {
			if c.Suit == card.Heart { // jokers (type=0) count toward the hearts tally too
				ht[c.Face-3]++
			}
		}
		groups[h.PosID] = handGroup{ID: h.PosID, Cards: h.Cards, HT: ht}
	}
	return gameStartPayload{Cards: groups}
}

func newGameOver(res game.Result) gameOverPayload {
	return gameOverPayload{Winner: res.Winner, Loser: res.Loser, Score: res.Score, Ratio: res.Ratio}
}

// posKeyMap: rebuild the {"0":x,...,"7":x} JS numeric-key object.
func posKeyMap(a [table.SeatCount]int) map[string]int {
	m := make(map[string]int, table.SeatCount)
	for i, v := range a {
		m[string(rune('0'+i))] = v
	}
	return m
}

func zeroPosMap() map[string]int {
	return posKeyMap(table.PlaySnapshot{}.SumFeng)
}

// leadFrame: opening lead / turn frame for a pre-lead disconnect — nothing
// to beat, turn to the leader, longer first-lead countdown.
func leadFrame(p int) ctxPlayChange {
	return ctxPlayChange{
		CtxData: ctxPlayCtx{Len: 0, Key: "", Type: "", Cards: []card.Card{}, PosID: p},
		SumFeng: zeroPosMap(),
		TmpFeng: 0,
		PosID:   p,
		Timeout: firstPlayTiming,
		Clear:   true, // leading state: clients clear the play area and "shape to beat"
	}
}

// playFrame: broadcast frame + reconnect snapshot after an accepted play
// (g is the post-application state).
func playFrame(g *game.Game, posID int, cards []card.Card, res game.PlayResult) (ctxPlayChange, *table.PlaySnapshot) {
	isPass := len(cards) == 0
	snap := &table.PlaySnapshot{
		IsPass:  isPass,
		Cards:   cards,
		PosID:   posID,
		SumFeng: g.SumFeng(),
		TmpFeng: g.TmpFeng(),
		Next:    g.Turn(),
	}
	var keyVal, typeVal interface{}
	if res.HasShape {
		keyVal, typeVal = res.Shape.Rank, res.Shape.TypeName()
		snap.Key, snap.Type = res.Shape.Rank, res.Shape.TypeName()
	} // on a pass, key/type stay absent (js compat)
	return ctxPlayChange{
		CtxData: ctxPlayCtx{Len: len(cards), Key: keyVal, Type: typeVal, Cards: nonNil(cards), PosID: posID},
		SumFeng: posKeyMap(snap.SumFeng),
		TmpFeng: snap.TmpFeng,
		PosID:   snap.Next,
		Timeout: playTiming,
		IsPass:  isPass,
	}, snap
}

// replayFrame: rebuild a frame from a reconnect snapshot.
func replayFrame(snap *table.PlaySnapshot) ctxPlayChange {
	var keyVal, typeVal interface{}
	if !snap.IsPass {
		keyVal, typeVal = snap.Key, snap.Type
	}
	return ctxPlayChange{
		CtxData: ctxPlayCtx{Len: len(snap.Cards), Key: keyVal, Type: typeVal, Cards: nonNil(snap.Cards), PosID: snap.PosID},
		SumFeng: posKeyMap(snap.SumFeng),
		TmpFeng: snap.TmpFeng,
		PosID:   snap.Next,
		Timeout: playTiming,
		IsPass:  snap.IsPass,
		Replay:  true,
	}
}

func ptrStr(v string) *string { return &v }

func nonNil(cards []card.Card) []card.Card {
	if cards == nil {
		return []card.Card{}
	}
	return cards
}
