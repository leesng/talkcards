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
		{"小对压过大单先出", mk(10, 4, 4), []int{4, 4}},
		{"小三压过大单先出", mk(10, 5, 5, 5), []int{5, 5, 5}},
		{"小牌出尽才出大对", mk(6, 6, 10, 10), []int{6, 6}},
		{"小双压过大于10的单先出", mk(11, 4, 4), []int{4, 4}},
		{"小三压过大于10的双先出", mk(5, 5, 5, 12, 12), []int{5, 5, 5}},
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
	cands := Candidates(append(mk(10, 10, 10, 10, 10), mk(6, 6, 6, 6)...))
	if len(cands) < 2 || len(cands[0]) != 4 || int(cands[0][0].Face) != 6 {
		t.Fatalf("4炸6 应排在 5炸10 前，实际 %v", cands)
	}
	cands = Candidates(append(mk(9, 9, 9, 9), mk(6, 6, 6, 6)...))
	if len(cands) < 2 || int(cands[0][0].Face) != 6 {
		t.Fatalf("同张数应先出小牌面，实际 %v", cands)
	}
}

// rngIs returns a random source that always yields v.
func rngIs(v float64) func() float64 { return func() float64 { return v } }

func TestFollowSmartTeammate(t *testing.T) {
	single9 := card.Shape{Kind: card.KindSingle, Rank: 9, Len: 1}
	single8 := card.Shape{Kind: card.KindSingle, Rank: 8, Len: 1}
	pairA := card.Shape{Kind: card.KindPair, Rank: 14, Len: 2}
	triple8 := card.Shape{Kind: card.KindTriple, Rank: 8, Len: 3}

	cases := []struct {
		name string
		ctx  FollowCtx
		want []int // nil = pass
	}{
		{"队友单9不吃", FollowCtx{Hand: mk(3, 3, 10), Top: single9, TopIsTeammate: true, Rand: rngIs(0.99)}, nil},
		{"队友对A不吃", FollowCtx{Hand: mk(3, 3, 4, 4), Top: pairA, TopIsTeammate: true, Rand: rngIs(0.99)}, nil},
		{"队友单8可吃", FollowCtx{Hand: mk(3, 3, 10), Top: single8, TopIsTeammate: true, Rand: rngIs(0.99)}, []int{10}},
		{"队友三8可吃", FollowCtx{Hand: mk(3, 3, 3, 9, 9, 9), Top: triple8, TopIsTeammate: true, Rand: rngIs(0.99)}, []int{9, 9, 9}},
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
	bomb4x6 := mk(6, 6, 6, 6)
	bomb4x10 := mk(10, 10, 10, 10)
	bomb5x := mk(10, 10, 10, 10, 10)
	bomb6x := mk(10, 10, 10, 10, 10, 10)
	king3 := mk(16, 16, 16) // 3-joker king bomb

	cases := []struct {
		name string
		hand []card.Card
		pot  int
		rand float64
		want []int
	}{
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

	hand := append(mk(7, 7), bomb4x6...)
	if cs := FollowSmart(FollowCtx{Hand: hand, Top: pair5, Pot: 0, Rand: rngIs(0.99)}); !reflect.DeepEqual(faces(cs), []int{7, 7}) {
		t.Fatalf("有对7可跟时不应掷骰出炸弹，实际 %v", faces(cs))
	}
}

// TestFollowSmartSplitHighPair splitting high pairs: with no whole regular
// group beating an opponent's single, an exact pair of A/2/jokers may split
// one card off to follow (the other stays in hand).
func TestFollowSmartSplitHighPair(t *testing.T) {
	singleK := card.Shape{Kind: card.KindSingle, Rank: 13, Len: 1}
	pairK := card.Shape{Kind: card.KindPair, Rank: 13, Len: 2}
	singleA := card.Shape{Kind: card.KindSingle, Rank: 14, Len: 1}
	singleJo := card.Shape{Kind: card.KindSingle, Rank: 16, Len: 1}
	single3 := card.Shape{Kind: card.KindSingle, Rank: 3, Len: 1}
	triple2 := card.Shape{Kind: card.KindTriple, Rank: 15, Len: 3}

	cases := []struct {
		name string
		ctx  FollowCtx
		want []int // nil = pass
	}{
		{"对手单K拆对A出单", FollowCtx{Hand: mk(14, 14, 5), Top: singleK, Rand: rngIs(0.99)}, []int{14}},
		{"对手单A拆对2出单", FollowCtx{Hand: mk(15, 15), Top: singleA, Rand: rngIs(0.99)}, []int{15}},
		{"对手单K拆对小王出单", FollowCtx{Hand: mk(16, 16), Top: singleK, Rand: rngIs(0.99)}, []int{16}},
		{"对手单小王拆对大王出单", FollowCtx{Hand: mk(17, 17), Top: singleJo, Rand: rngIs(0.99)}, []int{17}},
		{"有单5可跟不拆对A", FollowCtx{Hand: mk(5, 14, 14), Top: single3, Rand: rngIs(0.99)}, []int{5}},
		{"对手双K对A整组压", FollowCtx{Hand: mk(14, 14), Top: pairK, Rand: rngIs(0.99)}, []int{14, 14}},
		{"低牌对9不拆", FollowCtx{Hand: mk(9, 9), Top: singleK, Rand: rngIs(0.99)}, nil},
		// triples no longer split (the old triple-split rule was removed)
		{"三条A不拆", FollowCtx{Hand: mk(14, 14, 14), Top: singleK, Pot: 0, Rand: rngIs(0.99)}, nil},
		{"三条2不拆", FollowCtx{Hand: mk(15, 15, 15), Top: singleA, Pot: 0, Rand: rngIs(0.99)}, nil},
		{"4张A不拆走炸弹掷骰", FollowCtx{Hand: mk(14, 14, 14, 14), Top: singleK, Pot: 0, Rand: rngIs(0.99)}, nil},
		{"4张A必压档直接炸弹", FollowCtx{Hand: mk(14, 14, 14, 14), Top: singleK, Pot: 100, Rand: rngIs(0.99)}, []int{14, 14, 14, 14}},
		{"对手三2压不住A对过牌", FollowCtx{Hand: mk(14, 14, 5), Top: triple2, Pot: 0, Rand: rngIs(0.99)}, nil},
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
