package game

import (
	"reflect"
	"testing"

	"talkcards/backend/internal/card"
)

func newTestGame() *Game {
	g := &Game{rnd: func(n int) int { return 0 }} // 确定性随机
	g.Init()
	return g
}

func cards(vt ...int) []card.Card {
	cs := make([]card.Card, 0, len(vt)/2)
	for i := 0; i+1 < len(vt); i += 2 {
		cs = append(cs, card.Card{Face: card.Face(vt[i]), Suit: card.Suit(vt[i+1])})
	}
	return cs
}

func shape(kind card.Kind, rank, length int) card.Shape {
	return card.Shape{Kind: kind, Rank: rank, Len: length}
}

// 发牌守恒：总数 324、每人 40/41、恰好 4 人 41 张、有序
func TestInitCardsConservation(t *testing.T) {
	g := New()
	g.Start()

	total := 0
	fortyOne := 0
	for _, grp := range g.Hands() {
		n := len(grp.Cards)
		if n != 40 && n != 41 {
			t.Fatalf("pos %d 手牌 %d 张，应为 40/41", grp.PosID, n)
		}
		if n == 41 {
			fortyOne++
		}
		total += n
		for i := 1; i < len(grp.Cards); i++ {
			if grp.Cards[i-1].Face > grp.Cards[i].Face {
				t.Fatalf("pos %d 手牌未按牌面升序", grp.PosID)
			}
		}
	}
	if total != 324 {
		t.Fatalf("总牌数 %d != 324", total)
	}
	if fortyOne != 4 {
		t.Fatalf("41 张的人数 %d != 4", fortyOne)
	}
	if g.Phase() != PhasePlaying || g.Turn() < 0 || g.Turn() > 7 {
		t.Fatalf("start 后状态异常: phase=%v turn=%d", g.Phase(), g.Turn())
	}
	if g.TrickPos() != g.Turn() {
		t.Fatalf("开局先手即为桌面有效牌归属: trickPos=%d turn=%d", g.TrickPos(), g.Turn())
	}
	if g.DizhuPosID() != g.Turn() {
		t.Fatalf("首出者应为先手: lider=%d turn=%d", g.DizhuPosID(), g.Turn())
	}
}

// 确定性发牌：rnd 恒 0 时补牌给 2、6 号位
func TestInitCardsDeterministic(t *testing.T) {
	g := newTestGame()
	g.Start()
	want := []int{40, 40, 41, 41, 40, 40, 41, 41} // rnd恒0 → last4=[0,0,1,1,0,0,1,1]
	for i, grp := range g.Hands() {
		if len(grp.Cards) != want[i] {
			t.Fatalf("pos %d 手牌 %d != %d", i, len(grp.Cards), want[i])
		}
	}
}

// whoFirst：红桃 3 最多者先出；相同则比 3 总数；仍相同随机（rnd 恒 0 取最前者）
func TestWhoFirst(t *testing.T) {
	setHands := func(g *Game, hands ...[]card.Card) {
		for i, h := range hands {
			g.seats[i].Hand = h
		}
	}
	g := newTestGame()
	setHands(g,
		cards(3, 0), // 1 红桃3
		cards(4, 0),
		cards(5, 0),
		cards(6, 0),
		cards(7, 0),
		cards(3, 1, 3, 2, 3, 3), // 0 红桃3 但 3 张 3
		cards(3, 0, 3, 1),       // 1 红桃3 + 2 张 3
		cards(3, 0, 3, 1, 3, 2), // 1 红桃3 + 3 张 3
	)
	g.whoFirst()
	if g.Turn() != 7 {
		t.Fatalf("红桃3并列时应比 3 总数（7 号 3 张最多），实际 %d", g.Turn())
	}

	g2 := newTestGame()
	setHands(g2, cards(3, 0), cards(3, 1, 3, 2), cards(3, 0, 3, 0),
		nil, nil, nil, nil, nil)
	g2.whoFirst()
	if g2.Turn() != 2 {
		t.Fatalf("红桃 3 最多者（2 号）应先出，实际 %d", g2.Turn())
	}

	// 完全并列时随机：rnd 恒 0 → 取并列中最小座位号
	g3 := newTestGame()
	setHands(g3, cards(3, 1), nil, cards(3, 2), nil, cards(3, 3), nil, nil, nil)
	g3.whoFirst()
	if g3.Turn() != 0 {
		t.Fatalf("并列时 rnd(3)=0 应取 0 号，实际 %d", g3.Turn())
	}
	if g3.leader != 0 {
		t.Fatalf("并列时 rnd(3)=0 应取 0 号为先手，实际 %d", g3.leader)
	}
}

func playingGame() *Game {
	g := newTestGame()
	g.phase = PhasePlaying
	return g
}

// 压牌规则矩阵（经规则链校验）
func TestValidateMatrix(t *testing.T) {
	type cs struct {
		name  string
		trick Trick
		cards []card.Card
		ok    bool
	}
	lead := Trick{Pos: 3}
	cases := []cs{
		// 新一轮首出（自己是上一手出牌人）：任意合法牌型可出
		{"首出单张", lead, cards(5, 0), true},
		{"首出杂牌", lead, cards(5, 0, 6, 1), false},
		// 单张比大小：严格大于
		{"单张压单张", Trick{1, shape(card.KindSingle, 5, 1), 0}, cards(6, 0), true},
		{"单张不够大", Trick{1, shape(card.KindSingle, 5, 1), 0}, cards(4, 0), false},
		{"单张同值不可压", Trick{1, shape(card.KindSingle, 5, 1), 0}, cards(5, 1), false},
		// 对子
		{"对子压对子", Trick{1, shape(card.KindPair, 8, 2), 0}, cards(9, 0, 9, 1), true},
		{"对子压不动小对", Trick{1, shape(card.KindPair, 8, 2), 0}, cards(7, 0, 7, 1), false},
		{"单张不能压对子", Trick{1, shape(card.KindPair, 8, 2), 0}, cards(14, 0), false},
		{"三条不能压对子", Trick{1, shape(card.KindPair, 8, 2), 0}, cards(14, 0, 14, 1, 14, 2), false},
		// 炸弹
		{"炸弹压单张", Trick{1, shape(card.KindSingle, 14, 1), 0}, cards(4, 0, 4, 1, 4, 2, 4, 3), true},
		{"五炸压四炸", Trick{1, shape(card.KindBomb, 14, 4), 0}, cards(3, 0, 3, 1, 3, 2, 3, 3, 3, 0), true},
		{"四炸压不动四炸(同值)", Trick{1, shape(card.KindBomb, 14, 4), 0}, cards(14, 0, 14, 1, 14, 2, 14, 3), false},
		// 王炸 2N-1 规则（对非王炸）
		{"三王压≤5炸", Trick{1, shape(card.KindBomb, 14, 5), 0}, cards(16, 0, 16, 0, 16, 0), true},
		{"三王压不动6炸", Trick{1, shape(card.KindBomb, 14, 6), 0}, cards(16, 0, 16, 0, 16, 0), false},
		{"六王压≤11炸", Trick{1, shape(card.KindBomb, 14, 11), 0}, cards(17, 0, 17, 0, 17, 0, 17, 0, 17, 0, 17, 0), true},
		{"六王压不动12炸", Trick{1, shape(card.KindBomb, 14, 12), 0}, cards(17, 0, 17, 0, 17, 0, 17, 0, 17, 0, 17, 0), false},
		// 王炸对王炸：张数多者大；同张数大王 > 小王
		{"大王三压小王三", Trick{1, shape(card.KindKingBomb, 16, 3), 0}, cards(17, 0, 17, 0, 17, 0), true},
		{"小王三压不动大王三", Trick{1, shape(card.KindKingBomb, 17, 3), 0}, cards(16, 0, 16, 0, 16, 0), false},
		{"同级小王压不动小王", Trick{1, shape(card.KindKingBomb, 16, 3), 0}, cards(16, 0, 16, 0, 16, 0), false},
		{"四王压三王(张数多)", Trick{1, shape(card.KindKingBomb, 17, 3), 0}, cards(17, 0, 17, 0, 17, 0, 17, 0), true},
		{"三王压不动四王", Trick{1, shape(card.KindKingBomb, 16, 4), 0}, cards(17, 0, 17, 0, 17, 0), false},
		{"四王压不动六王", Trick{1, shape(card.KindKingBomb, 17, 6), 0}, cards(16, 0, 16, 0, 16, 0, 16, 0), false},
		// 王炸被普通炸弹反压：需张数 > 2N-1（王的折算数）
		{"四炸压不动三王(4≤5)", Trick{1, shape(card.KindKingBomb, 17, 3), 0}, cards(4, 0, 4, 1, 4, 2, 4, 3), false},
		{"五炸压不动三王(5≤5)", Trick{1, shape(card.KindKingBomb, 17, 3), 0}, cards(4, 0, 4, 1, 4, 2, 4, 3, 4, 0), false},
		{"六炸反压三王(6>5)", Trick{1, shape(card.KindKingBomb, 17, 3), 0}, cards(4, 0, 4, 1, 4, 2, 4, 3, 4, 0, 4, 1), true},
		{"七炸压不动四王(7≤7)", Trick{1, shape(card.KindKingBomb, 16, 4), 0}, cards(4, 0, 4, 1, 4, 2, 4, 3, 4, 0, 4, 1, 4, 2), false},
		{"八炸反压四王(8>7)", Trick{1, shape(card.KindKingBomb, 16, 4), 0}, cards(4, 0, 4, 1, 4, 2, 4, 3, 4, 0, 4, 1, 4, 2, 4, 3), true},
		{"三炸不是炸弹压不了王炸", Trick{1, shape(card.KindKingBomb, 17, 3), 0}, cards(4, 0, 4, 1, 4, 2), false},
		{"两对杂牌压不了任何", Trick{1, shape(card.KindKingBomb, 17, 3), 0}, cards(4, 0, 4, 1, 5, 0, 5, 1), false},
		// 王的多重集双解读
		{"三小王作为三条压三2", Trick{1, shape(card.KindTriple, 15, 3), 0}, cards(16, 0, 16, 0, 16, 0), true},
		{"四小王作为四炸压四A", Trick{1, shape(card.KindBomb, 14, 4), 0}, cards(16, 0, 16, 0, 16, 0, 16, 0), true},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			g := playingGame()
			g.seats[3].Hand = bigHand()
			g.turn = 3
			g.trick = c.trick
			ctx := g.validatePlay(3, c.cards)
			if (ctx != nil) != c.ok {
				t.Fatalf("validatePlay = %v, 期望 ok=%v", ctx, c.ok)
			}
		})
	}
}

// bigHand 构造包含各种需要牌的手牌
func bigHand() []card.Card {
	return cards(
		3, 0, 3, 1, 3, 2, 3, 3, 3, 0,
		4, 0, 4, 1, 4, 2, 4, 3, 4, 0,
		5, 0, 6, 0, 6, 1, 7, 0, 8, 0, 8, 1,
		9, 0, 9, 1, 14, 0, 14, 1, 14, 2, 14, 3,
		16, 0, 16, 0, 16, 0, 17, 0, 17, 0, 17, 0,
		17, 0, 17, 0, 17, 0, 4, 0, 4, 1, 4, 2, 4, 3,
	)
}

// 防作弊：不在手上的牌不能出；
// bug-for-bug：手上一张 5♥ 重复出两张组成"对子"，ruleHandHas 每张独立查手牌不拦
// （旧 checkExist 语义保留），首出场景下可借此虚报牌——与旧实现一致
func TestValidateCheat(t *testing.T) {
	g := playingGame()
	g.seats[0].Hand = cards(5, 0)
	g.turn = 0
	g.trick = Trick{Pos: 1, Top: shape(card.KindSingle, 4, 1)} // 桌面单 4
	if g.validatePlay(0, cards(5, 1)) != nil {
		t.Fatal("不在手上的牌不应通过")
	}

	g2 := playingGame()
	g2.seats[0].Hand = cards(5, 0)
	g2.turn = 0
	g2.trick = Trick{Pos: 0} // 自己首出
	if g2.validatePlay(0, cards(5, 0, 5, 0)) == nil {
		t.Fatal("旧 checkExist 语义：重复出同一张具体牌不被拦截（bug-for-bug）")
	}
}

// 计分牌
func TestScoreOf(t *testing.T) {
	if got := scoreOf(cards(5, 0, 10, 1, 13, 2, 3, 3)); got != 25 {
		t.Fatalf("scoreOf = %d, want 25", got)
	}
}

// 越序：合法牌越序 → 受理但不应用，对局毒化；不合法牌 → 不受理不毒化
func TestWrongTurn(t *testing.T) {
	g := playingGame()
	g.seats[3].Hand = cards(5, 0)
	g.turn = 1
	g.trick = Trick{Pos: 1, Top: shape(card.KindSingle, 4, 1)}
	res := g.Play(3, cards(5, 0))
	if !res.Accepted || res.Applied {
		t.Fatalf("越序合法牌应 Accepted&&!Applied，实际 %+v", res)
	}
	if g.Phase() != PhaseError {
		t.Fatalf("越序应毒化对局，实际 %v", g.Phase())
	}

	g2 := playingGame()
	g2.turn = 1
	res2 := g2.Play(3, cards(5, 0)) // 牌不在手
	if res2.Accepted || g2.Phase() != PhasePlaying {
		t.Fatalf("不合法牌不应毒化: res=%+v phase=%v", res2, g2.Phase())
	}
}

// 轮转与抓分：首出 5 分牌，其余过牌，一圈后分数归首出者
func TestTrickScoring(t *testing.T) {
	g := playingGame()
	for i, h := range [][]int{
		{5, 0, 3, 1}, {4, 0}, {15, 0}, {15, 1},
		{15, 2}, {15, 3}, {15, 0}, {15, 1},
	} {
		g.seats[i].Hand = cards(h...)
	}
	g.turn = 0
	g.trick = Trick{Pos: 0}

	g.Play(0, cards(5, 0)) // 出 5 分牌
	if g.Turn() != 1 {
		t.Fatalf("轮转应到 1，实际 %d", g.Turn())
	}
	for p := 1; p <= 7; p++ { // 1-7 依次过牌
		g.Play(p, nil)
	}
	if g.Turn() != 0 {
		t.Fatalf("全部过牌后应轮回 0，实际 %d", g.Turn())
	}
	if g.SumFeng()[0] != 5 {
		t.Fatalf("0 号应抓 5 分，实际 %v", g.SumFeng())
	}
	if g.TmpFeng() != 0 {
		t.Fatalf("本轮分应清零，实际 %d", g.TmpFeng())
	}
}

// 出完牌让队友接手 + 双队全出完终局
func TestGameOverByRunOut(t *testing.T) {
	g := playingGame()
	for i, h := range [][]int{
		{3, 0}, {4, 0}, {5, 0}, {6, 0}, {7, 0}, {8, 0}, {9, 0}, {10, 0},
	} {
		g.seats[i].Hand = cards(h...)
	}
	g.turn = 0
	g.trick = Trick{Pos: 0}

	for _, pos := range []int{0, 2, 4, 6} {
		g.Play(pos, g.seats[pos].Hand) // 各出唯一一张，出完让下个队友接风
	}
	if g.Phase() != PhaseOver {
		t.Fatalf("偶数队出完应终局，实际 %v", g.Phase())
	}
	r := g.Result()
	if !reflect.DeepEqual(r.Winner, []int{0, 2, 4, 6}) || !reflect.DeepEqual(r.Loser, []int{1, 3, 5, 7}) {
		t.Fatalf("胜负方错误: %+v", r)
	}
	if r.Ratio != 1 {
		t.Fatalf("默认 1 倍, 实际 %d", r.Ratio)
	}
	// 全队出完带走对方未出手分牌：败方仅 7 号剩 10♥（10 分），胜方自抓 0 分
	if r.Score != 10 {
		t.Fatalf("最终得分应带走败方 10 分，实际 %d", r.Score)
	}
	t0, t1 := g.TeamScores()
	if t0 != 10 || t1 != 0 {
		t.Fatalf("TeamScores 应为 (10,0)，实际 (%d,%d)", t0, t1)
	}
}

// 出完者累计 ≥300 分终局（尚有队友未出完时走 300 分线，不带走败方剩牌分）
func TestGameOverByScore(t *testing.T) {
	g := playingGame()
	for i, h := range [][]int{
		nil, {4, 0}, nil, {6, 0}, {5, 0}, {8, 0}, {9, 0}, {10, 0},
	} {
		if h != nil {
			g.seats[i].Hand = cards(h...)
		}
	}
	g.turn = 6
	g.seats[0].Captured = 200
	g.seats[2].Captured = 100
	g.trick = Trick{Pos: 6}
	g.Play(6, cards(9, 0)) // 6 出完最后一张 → 偶数队出完者 300 分
	if g.Phase() != PhaseOver {
		t.Fatalf("偶数队 ≥300 应终局，实际 %v", g.Phase())
	}
	if !reflect.DeepEqual(g.Result().Winner, []int{0, 2, 4, 6}) {
		t.Fatal("偶数队应胜")
	}
	if g.Result().Score != 300 {
		t.Fatalf("300 分线终局得分应为 300（不带走败方剩牌），实际 %d", g.Result().Score)
	}
}

// nextGroupPosID：找不到有牌队友返回 -1（js 为 undefined）
func TestNextGroupPosIDNone(t *testing.T) {
	g := playingGame()
	for i, h := range [][]int{
		nil, {4, 0}, nil, {6, 0}, nil, {8, 0}, nil, {10, 0},
	} {
		if h != nil {
			g.seats[i].Hand = cards(h...)
		}
	}
	if got := g.nextGroupPosID(0); got != -1 {
		t.Fatalf("无队友有牌应返回 -1，实际 %d", got)
	}
}

// 接风立即触发：打出最后一手牌后本轮余家无权压，由同队下一位有牌队友接风；
// 桌面未收分不清算，由下一圈赢墩者收取（game-rules.md 接风节）
func TestWindRelayImmediate(t *testing.T) {
	g := playingGame()
	for i, h := range [][]int{
		{5, 0}, {14, 0}, {7, 0, 8, 0}, {3, 0}, {3, 1}, {3, 2}, {3, 3}, {4, 0},
	} {
		g.seats[i].Hand = cards(h...)
	}
	g.turn = 0
	g.trick = Trick{Pos: 0}

	g.Play(0, cards(5, 0)) // 0 号最后一张是分牌
	if g.Turn() != 2 {
		t.Fatalf("接风应直接轮到同队 2 号（跳过 1），实际 %d", g.Turn())
	}
	if g.TrickPos() != 2 {
		t.Fatalf("接风后 2 号成为首出者，实际 trickPos=%d", g.TrickPos())
	}
	if g.TmpFeng() != 5 {
		t.Fatalf("接风时桌面分不清算，实际 %d", g.TmpFeng())
	}

	// 2 号任意首出 7，全场过牌（含被跳过轮次的 1 号）回到 2 → 收走含 0 号 5 分的整圈
	g.Play(2, cards(7, 0))
	for _, p := range []int{3, 4, 5, 6, 7, 1} {
		g.Play(p, nil)
	}
	if g.Turn() != 2 {
		t.Fatalf("一圈过牌后应回到 2，实际 %d", g.Turn())
	}
	if g.SumFeng()[2] != 5 || g.TmpFeng() != 0 {
		t.Fatalf("2 号应收取整圈 5 分: sumFeng=%v tmpFeng=%d", g.SumFeng(), g.TmpFeng())
	}
}

// 全队出完终局：胜队只带走败方手牌分，桌面滚动中的 Pot（含最后一手打出的分）不计入
func TestGameOverByRunOutDropsPot(t *testing.T) {
	g := playingGame()
	for i, h := range [][]int{
		{5, 0}, {3, 0}, {5, 1}, {3, 1}, {5, 2}, {3, 2}, {5, 3}, {10, 0},
	} {
		g.seats[i].Hand = cards(h...)
	}
	g.turn = 0
	g.trick = Trick{Pos: 0}

	for _, pos := range []int{0, 2, 4, 6} {
		g.Play(pos, g.seats[pos].Hand) // 各打出唯一 5 分牌，桌面滚 20 分
	}
	if g.Phase() != PhaseOver {
		t.Fatalf("偶数队出完应终局，实际 %v", g.Phase())
	}
	if g.TmpFeng() != 20 {
		t.Fatalf("终局时桌面应滚着 20 分，实际 %d", g.TmpFeng())
	}
	if got := g.Result().Score; got != 10 {
		t.Fatalf("应只带走败方手牌 10 分（Pot 20 分丢弃），实际 %d", got)
	}
}

// 299 分不触发 300 分线；未出完者收分再高也不计入胜负线
func TestGameOverByScoreNotReached(t *testing.T) {
	g := playingGame()
	for i, h := range [][]int{
		nil, {4, 0}, nil, {6, 0}, {5, 0}, {8, 0}, {9, 0}, {10, 0},
	} {
		if h != nil {
			g.seats[i].Hand = cards(h...)
		}
	}
	g.turn = 6
	g.seats[0].Captured = 200
	g.seats[2].Captured = 99  // 出完者合计 299
	g.seats[1].Captured = 400 // 未出完者收分不计入
	g.trick = Trick{Pos: 6}
	g.Play(6, cards(9, 0))
	if g.Phase() != PhasePlaying {
		t.Fatalf("299 分不应终局，实际 %v", g.Phase())
	}
}

// 首出者过牌（空手牌一律放行）：轮转照常推进，桌面牌不变
func TestPassOnOwnLead(t *testing.T) {
	g := playingGame()
	for i, h := range [][]int{
		{5, 0, 3, 0}, {6, 0}, {4, 0}, {4, 1}, {4, 2}, {4, 3}, {4, 0}, {4, 1},
	} {
		g.seats[i].Hand = cards(h...)
	}
	g.turn = 0
	g.trick = Trick{Pos: 0, Top: shape(card.KindSingle, 5, 1)}

	res := g.Play(0, nil)
	if !res.Accepted || !res.Applied || g.Turn() != 1 {
		t.Fatalf("首出者过牌后应轮到 1，实际 %d（%+v）", g.Turn(), res)
	}
	if g.TrickPos() != 0 || g.trick.Top.Rank != 5 {
		t.Fatalf("桌面牌应保持不变: %+v", g.trick)
	}
	if g.validatePlay(1, cards(4, 0)) != nil {
		t.Fatal("4 压不动桌面 5，桌面牌未被首出者过牌清掉")
	}
	if g.validatePlay(1, cards(6, 0)) == nil {
		t.Fatal("6 应能压桌面 5")
	}
}
