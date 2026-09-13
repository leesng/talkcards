// Package bot 人机模式机器人策略：与 e2e-audit.js 的 candidates() 及前端
// parser.js 的提示（findHintCards）同源——按「孤张单 → 对 → 三 → 炸弹」整组取牌，
// 不拆对/三；炸弹桶先按张数升序、同张数再按牌面升序。跟不住则过牌。
package bot

import (
	"math/rand"
	"sort"

	"talkcards/backend/internal/card"
)

// Candidates 手牌候选组：每个牌值按现有张数整体归组（1=孤张单 2=对 3=三，
// ≥4 张或 3 张以上同王=炸弹），返回按组序排列的候选，每项为该组全部牌。
func Candidates(hand []card.Card) [][]card.Card {
	byFace := make(map[card.Face][]card.Card)
	for _, c := range hand {
		byFace[c.Face] = append(byFace[c.Face], c)
	}
	faces := make([]card.Face, 0, len(byFace))
	for f := range byFace {
		faces = append(faces, f)
	}
	sort.Slice(faces, func(i, j int) bool { return faces[i] < faces[j] })

	buckets := [4][][]card.Card{} // 单/对/三/炸弹
	for _, f := range faces {
		cs := byFace[f]
		gi := len(cs) - 1
		if f.IsJoker() && len(cs) >= 3 {
			gi = 3 // 3 张以上同王按王炸处理，不当作三条
		}
		if gi > 3 {
			gi = 3
		}
		buckets[gi] = append(buckets[gi], cs)
	}
	// 炸弹组：先按张数升序（4炸<5炸<6炸…），同张数再按牌面升序
	sort.SliceStable(buckets[3], func(i, j int) bool {
		if len(buckets[3][i]) != len(buckets[3][j]) {
			return len(buckets[3][i]) < len(buckets[3][j])
		}
		return buckets[3][i][0].Face < buckets[3][j][0].Face
	})

	out := make([][]card.Card, 0, len(hand))
	for _, b := range buckets {
		out = append(out, b...)
	}
	return out
}

// Lead 自由首出（一轮第一手）：≤8 的小牌（单/双/三不限型）按牌面升序最优先，
// 先把小单/小对/小三混杂着清完；小牌出尽后再按组序（单→对→三→炸弹）整组出。
func Lead(hand []card.Card) []card.Card {
	cands := Candidates(hand)
	best := -1
	for i, cs := range cands {
		if len(cs) > 3 {
			break // 之后只剩炸弹组，不参与小牌优先
		}
		if cs[0].Face <= 8 && (best == -1 || cs[0].Face < cands[best][0].Face) {
			best = i
		}
	}
	if best >= 0 {
		return cands[best]
	}
	if len(cands) == 0 {
		return nil
	}
	return cands[0]
}

// Follow 跟牌：按组序找第一组整体解读后能压住 top 的；全组压不住返回 nil（过牌）
func Follow(hand []card.Card, top card.Shape) []card.Card {
	for _, cs := range Candidates(hand) {
		for _, s := range card.Classify(cs) {
			if s.Beats(top) {
				return cs
			}
		}
	}
	return nil
}

// FollowCtx 智能跟牌上下文。
type FollowCtx struct {
	Hand          []card.Card
	Top           card.Shape     // 当前桌面有效牌型（被压目标）
	TopIsTeammate bool           // 上一手出牌者是否队友（同奇偶座位）
	Pot           int            // 桌面滚动分（TmpFeng）
	Rand          func() float64 // [0,1) 随机源，nil 用 math/rand 全局（测试可注入）
}

// FollowSmart 智能跟牌（非首出时）：
//  1. 队友的牌：除非是 ≤8 的单/双/三，一律不吃（只吃对手的牌）；
//  2. 只有炸弹能大过且桌面无分：≤4×8 的炸弹 30% 出，4×8 以上的 4/5 张炸弹 5% 出，
//     超过 5 张或王炸不出；
//  3. 桌面有分：分 ≥50 必压；分 <50 时 4 张炸弹照出、5 张 50%、6 张及以上（含王炸）20%。
//
// 非炸弹的常规跟牌不受掷骰限制（能压就用最小的一组）；返回 nil 表示过牌。
func FollowSmart(ctx FollowCtx) []card.Card {
	// 规则 1：不吃队友的大牌，只吃对手的牌
	if ctx.TopIsTeammate && !isSmallTop(ctx.Top) {
		return nil
	}

	// 候选按组序（单→对→三→炸弹，炸弹张数/牌面升序）扫描：
	// 第一个能压的非炸弹 = 最小常规跟牌；第一个能压的炸弹 = 最小炸弹
	var normal, bomb []card.Card
	var bombShape card.Shape
	for _, cs := range Candidates(ctx.Hand) {
		for _, s := range card.Classify(cs) {
			if !s.Beats(ctx.Top) {
				continue
			}
			if s.Kind == card.KindBomb || s.Kind == card.KindKingBomb {
				if bomb == nil {
					bomb, bombShape = cs, s
				}
			} else if normal == nil {
				normal = cs
			}
		}
	}
	if normal != nil {
		return normal
	}
	if bomb == nil {
		return nil // 跟不住 → 过牌
	}

	// 只剩炸弹能大过：按桌面分掷骰
	rng := ctx.Rand
	if rng == nil {
		rng = rand.Float64
	}
	if rng() < bombChance(bombShape, ctx.Pot) {
		return bomb
	}
	return nil
}

// isSmallTop 队友出的牌是否为 ≤8 的单/双/三（小牌可以让队友吃走拿牌权）
func isSmallTop(top card.Shape) bool {
	switch top.Kind {
	case card.KindSingle, card.KindPair, card.KindTriple:
		return top.Rank <= 8
	}
	return false
}

// bombChance 只剩炸弹时各炸弹的放行概率。
func bombChance(b card.Shape, pot int) float64 {
	if pot > 50 {
		return 1 // 分大于 50 必压过对手
	}
	king := b.Kind == card.KindKingBomb
	if pot == 0 {
		// 桌面没分：≤4×8 两成，4 张大牌面或超 4 张（含王炸）半成
		switch {
		case king || b.Len > 4:
			return 0.05
		case b.Rank <= 8:
			return 0.20
		default:
			return 0.10
		}
	}
	// 桌面有分且 ≤50：4 张八成，5 张四成，6 张及以上（含王炸）一成
	switch {
	case king || b.Len >= 6:
		return 0.10
	case b.Len == 5:
		return 0.40
	default:
		return 0.80
	}
}
