// translator.go wire 载荷翻译：领域状态（game/table）→ 事件载荷结构，
// 以及快照重建（断线重连重放）。内部数组快照在此重建 JS 数字键对象形态。
package hub

import (
	"talkcards/backend/internal/card"
	"talkcards/backend/internal/game"
	"talkcards/backend/internal/table"
)

// newGameStart 组装 GAME_START 载荷：领域手牌快照 → wire 分组（含红桃统计）
func newGameStart(g *game.Game) gameStartPayload {
	hands := g.Hands()
	groups := make([]handGroup, len(hands))
	for _, h := range hands {
		ht := [15]int{}
		for _, c := range h.Cards {
			if c.Suit == card.Heart { // 大小王 type=0 也计入红桃统计
				ht[c.Face-3]++
			}
		}
		groups[h.PosID] = handGroup{ID: h.PosID, Cards: h.Cards, HT: ht}
	}
	return gameStartPayload{Cards: groups}
}

// newGameOver 领域终局结果 → GAME_OVER wire 载荷
func newGameOver(res game.Result) gameOverPayload {
	return gameOverPayload{Winner: res.Winner, Loser: res.Loser, Score: res.Score, Ratio: res.Ratio}
}

// posKeyMap 重建 {"0":x,...,"7":x} 数字键对象（与 JS 键序一致）
func posKeyMap(a [table.SeatCount]int) map[string]int {
	m := make(map[string]int, table.SeatCount)
	for i, v := range a {
		m[string(rune('0'+i))] = v
	}
	return m
}

// zeroPosMap 全零抓分快照（开局/引导帧）
func zeroPosMap() map[string]int {
	return posKeyMap(table.PlaySnapshot{}.SumFeng)
}

// leadFrame 开局首出引导帧/首出前掉线的补发轮转帧：
// 无压牌（key/type 为空串）、轮转到首出者
func leadFrame(p int) ctxPlayChange {
	return ctxPlayChange{
		CtxData: ctxPlayCtx{Len: 0, Key: "", Type: "", Cards: []card.Card{}, PosID: p},
		SumFeng: zeroPosMap(),
		TmpFeng: 0,
		PosID:   p,
		Timeout: playTiming,
		Clear:   true, // 领出状态：桌面无牌可压，客户端应清空出牌区与"要压的牌型"
	}
}

// playFrame 出牌受理后的广播帧与重连快照（g 须为已应用该手牌后的状态）
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
	} // 过牌时与 js 一致：key/type 不出现在 payload 中
	return ctxPlayChange{
		CtxData: ctxPlayCtx{Len: len(cards), Key: keyVal, Type: typeVal, Cards: nonNil(cards), PosID: posID},
		SumFeng: posKeyMap(snap.SumFeng),
		TmpFeng: snap.TmpFeng,
		PosID:   snap.Next,
		Timeout: playTiming,
		IsPass:  isPass,
	}, snap
}

// replayFrame 由重连快照重建广播帧（刷新倒计时）
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
