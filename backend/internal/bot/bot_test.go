package bot

import (
	"reflect"
	"testing"

	"talkcards/backend/internal/card"
)

func mk(vs ...int) []card.Card {
	out := make([]card.Card, len(vs))
	for i, v := range vs {
		out[i] = card.Card{Face: card.Face(v), Suit: card.Suit(i % 4)}
	}
	return out
}

func faces(cs []card.Card) []int {
	out := make([]int, len(cs))
	for i, c := range cs {
		out[i] = int(c.Face)
	}
	return out
}

func TestLeadAtomicGroup(t *testing.T) {
	cases := []struct {
		name string
		hand []card.Card
		want []int
	}{
		{"小牌面优先对3先于单4", mk(3, 3, 4), []int{3, 3}},
		{"无孤张取最小对", mk(3, 3, 4, 4), []int{3, 3}},
		{"有孤张单仍不拆三", mk(5, 5, 5, 4), []int{4}},
		{"无单无对取最小三", mk(6, 6, 6, 5, 5, 5), []int{5, 5, 5}},
		{"不拆炸弹", mk(7, 7, 7, 7, 8), []int{8}},
		{"只剩炸弹兜底", mk(5, 5, 5, 5), []int{5, 5, 5, 5}},
		{"3+同王按王炸不抢首出", mk(3, 3, 16, 16, 16), []int{3, 3}},
		// 自由首出：≤8 小牌按牌面优先，单/双/三不限型混着清
		{"小对压过大单先出", mk(10, 4, 4), []int{4, 4}},
		{"小三压过大单先出", mk(10, 5, 5, 5), []int{5, 5, 5}},
		{"小牌出尽才出大对", mk(6, 6, 10, 10), []int{6, 6}},
	}
	for _, c := range cases {
		if got := faces(Lead(c.hand)); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: got %v want %v", c.name, got, c.want)
		}
	}
}

func TestFollowAtomic(t *testing.T) {
	single5 := card.Shape{Kind: card.KindSingle, Rank: 5, Len: 1}
	tripleA := card.Shape{Kind: card.KindTriple, Rank: 14, Len: 3}
	bomb5x4 := card.Shape{Kind: card.KindBomb, Rank: 5, Len: 4}
	bomb5x5 := card.Shape{Kind: card.KindBomb, Rank: 5, Len: 5}

	cases := []struct {
		name string
		hand []card.Card
		top  card.Shape
		want []int
	}{
		{"跟单不拆对", mk(6, 6, 7), single5, []int{7}},
		{"跟单不拆三", mk(6, 6, 6, 7), single5, []int{7}},
		{"跟不住对2则过牌", mk(6, 7, 7), card.Shape{Kind: card.KindPair, Rank: 15, Len: 2}, nil},
		{"跟不住单则过牌", mk(3, 3, 4), card.Shape{Kind: card.KindSingle, Rank: 15, Len: 1}, nil},
		{"跟三5取整三6", mk(6, 6, 6), card.Shape{Kind: card.KindTriple, Rank: 5, Len: 3}, []int{6, 6, 6}},
		{"炸弹张数优先", mk(6, 6, 6, 6, 10, 10, 10, 10, 10), tripleA, []int{6, 6, 6, 6}},
		{"跟炸同张数取更大牌面", mk(7, 7, 7, 7, 10, 10, 10, 10, 10), bomb5x4, []int{7, 7, 7, 7}},
		{"跟5张炸需更多张数", mk(7, 7, 7, 7, 10, 10, 10, 10, 10), bomb5x5, []int{10, 10, 10, 10, 10}},
		{"3王=王炸可压三", mk(6, 6, 6, 16, 16, 16), tripleA, []int{16, 16, 16}},
	}
	for _, c := range cases {
		var got []int
		if cs := Follow(c.hand, c.top); cs != nil {
			got = faces(cs)
		}
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: got %v want %v", c.name, got, c.want)
		}
	}
}

func TestBombOrdering(t *testing.T) {
	// 候选顺序：张数优先 4炸6 在 5炸10 前；同张数按牌面 4炸6 在 4炸9 前
	cands := Candidates(append(mk(10, 10, 10, 10, 10), mk(6, 6, 6, 6)...))
	if len(cands) < 2 || len(cands[0]) != 4 || int(cands[0][0].Face) != 6 {
		t.Fatalf("4炸6 应排在 5炸10 前，实际 %v", cands)
	}
	cands = Candidates(append(mk(9, 9, 9, 9), mk(6, 6, 6, 6)...))
	if len(cands) < 2 || int(cands[0][0].Face) != 6 {
		t.Fatalf("同张数应先出小牌面，实际 %v", cands)
	}
}


// rngIs 构造恒返回 v 的随机源
func rngIs(v float64) func() float64 { return func() float64 { return v } }

func TestFollowSmartTeammate(t *testing.T) {
	single9 := card.Shape{Kind: card.KindSingle, Rank: 9, Len: 1}
	single8 := card.Shape{Kind: card.KindSingle, Rank: 8, Len: 1}
	pairA := card.Shape{Kind: card.KindPair, Rank: 14, Len: 2}
	triple8 := card.Shape{Kind: card.KindTriple, Rank: 8, Len: 3}

	cases := []struct {
		name string
		ctx  FollowCtx
		want []int // nil = 过牌
	}{
		// 规则 1：队友大牌不吃
		{"队友单9不吃", FollowCtx{Hand: mk(3, 3, 10), Top: single9, TopIsTeammate: true, Rand: rngIs(0.99)}, nil},
		{"队友对A不吃", FollowCtx{Hand: mk(3, 3, 4, 4), Top: pairA, TopIsTeammate: true, Rand: rngIs(0.99)}, nil},
		// 队友 ≤8 的单/双/三可以吃
		{"队友单8可吃", FollowCtx{Hand: mk(3, 3, 10), Top: single8, TopIsTeammate: true, Rand: rngIs(0.99)}, []int{10}},
		{"队友三8可吃", FollowCtx{Hand: mk(3, 3, 3, 9, 9, 9), Top: triple8, TopIsTeammate: true, Rand: rngIs(0.99)}, []int{9, 9, 9}},
		// 对手的牌照常吃
		{"对手单9必吃", FollowCtx{Hand: mk(3, 3, 10), Top: single9, Rand: rngIs(0.99)}, []int{10}},
		{"队友炸弹不吃(非小牌)", FollowCtx{Hand: mk(4, 4, 4, 4), Top: card.Shape{Kind: card.KindBomb, Rank: 3, Len: 4}, TopIsTeammate: true, Pot: 50, Rand: rngIs(0)}, nil},
	}
	for _, c := range cases {
		var got []int
		if cs := FollowSmart(c.ctx); cs != nil {
			got = faces(cs)
		}
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: got %v want %v", c.name, got, c.want)
		}
	}
}

func TestFollowSmartBombChance(t *testing.T) {
	pair5 := card.Shape{Kind: card.KindPair, Rank: 5, Len: 2}
	bomb4x6 := mk(6, 6, 6, 6)            // 4×6 ≤4×8
	bomb4x10 := mk(10, 10, 10, 10)       // 4×10 >4×8
	bomb5x := mk(10, 10, 10, 10, 10)     // 5 张
	bomb6x := mk(10, 10, 10, 10, 10, 10) // 6 张
	king3 := mk(16, 16, 16)              // 3 王王炸

	// 规则 2：桌面无分掷骰
	cases := []struct {
		name string
		hand []card.Card
		pot  int
		rand float64
		want []int
	}{
		// 规则 2：桌面无分 ≤4×8 炸弹 20%、4 张大牌面 10%、超 4 张（含王炸）5%
		{"无分4x8内20%命中", bomb4x6, 0, 0.19, []int{6, 6, 6, 6}},
		{"无分4x8内20%未中", bomb4x6, 0, 0.20, nil},
		{"无分4x10仅10%命中", bomb4x10, 0, 0.09, []int{10, 10, 10, 10}},
		{"无分4x10仅10%未中", bomb4x10, 0, 0.10, nil},
		{"无分5张仅5%未中", bomb5x, 0, 0.05, nil},
		{"无分5张5%命中", bomb5x, 0, 0.04, []int{10, 10, 10, 10, 10}},
		{"无分6张仅5%", bomb6x, 0, 0.06, nil},
		{"无分王炸仅5%", king3, 0, 0.04, []int{16, 16, 16}},
		{"无分王炸5%未中", king3, 0, 0.05, nil},
		{"有分49 5张40%未中", bomb5x, 49, 0.40, nil},
		{"有分49 5张40%命中", bomb5x, 49, 0.39, []int{10, 10, 10, 10, 10}},
		{"有分49 6张10%命中", bomb6x, 49, 0.09, []int{10, 10, 10, 10, 10, 10}},
		{"有分49 6张10%未中", bomb6x, 49, 0.10, nil},
		{"有分49 4张80%命中", bomb4x10, 49, 0.79, []int{10, 10, 10, 10}},
		{"有分49 4张80%未中", bomb4x10, 49, 0.80, nil},
		{"有分50 6张仍10%", bomb6x, 50, 0.10, nil},
		{"有分50 5张仍40%", bomb5x, 50, 0.40, nil},
		{"有分50 4张仍80%", bomb4x10, 50, 0.80, nil},
		{"有分51必压", bomb6x, 51, 0.99, []int{10, 10, 10, 10, 10, 10}},
	}
	for _, c := range cases {
		ctx := FollowCtx{Hand: c.hand, Top: pair5, Pot: c.pot, Rand: rngIs(c.rand)}
		var got []int
		if cs := FollowSmart(ctx); cs != nil {
			got = faces(cs)
		}
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: got %v want %v", c.name, got, c.want)
		}
	}

	// 有常规跟牌时不掷骰直接出最小非炸弹
	hand := append(mk(7, 7), bomb4x6...)
	if cs := FollowSmart(FollowCtx{Hand: hand, Top: pair5, Pot: 0, Rand: rngIs(0.99)}); !reflect.DeepEqual(faces(cs), []int{7, 7}) {
		t.Fatalf("有对7可跟时不应掷骰出炸弹，实际 %v", faces(cs))
	}
}
