// Package game 一桌牌局的聚合根：发牌、先手、规则链校验、轮转、抓分、接风、终局。
// 纯领域零 I/O，不感知连接与广播；非并发安全，由 hub 的互斥锁保护。
// 行为对齐旧实现（game.js 移植版），wire 帧由 hub 翻译。
package game

import (
	"math/rand"
	"sort"

	"talkcards/backend/internal/card"
)

// Phase 对局阶段（数值沿用旧 status 0-5）
type Phase int

const (
	PhaseIdle    Phase = 0
	PhaseCall    Phase = 1 // 叫分
	PhasePlaying Phase = 2 // 出牌
	PhaseOver    Phase = 3 // 结束
	PhaseRedeal  Phase = 4 // 需要重发
	PhaseError   Phase = 5
)

// HandSnapshot 一家手牌的领域快照；wire 形态（id/cards/ht，HT 为红桃统计）
// 由 hub/translator 组装，本包不感知 JSON 字段名
type HandSnapshot struct {
	PosID int
	Cards []card.Card
}

// Seat 游戏中的一名玩家
type Seat struct {
	PosID    int
	Hand     []card.Card
	Called   int // 叫分：-1 未叫，0-3（先手默认叫满 3）
	Captured int // 抓分（只有出完者的抓分计入胜负线）
}

// Team 队伍：单数位 1/3/5/7 对双数位 0/2/4/6
func (s *Seat) Team() int { return s.PosID % 2 }

// Out 是否出完手牌
func (s *Seat) Out() bool { return len(s.Hand) == 0 }

// Trick 桌面状态：当前有效牌与滚动分。Pos 为有效牌归属座位，
// -1 表示无（不可达的初始态；一旦进入出牌阶段恒有归属）。
type Trick struct {
	Pos int
	Top card.Shape // 当前有效牌型（接风后为陈旧值，Pos 匹配判断优先于 Top）
	Pot int        // 桌面滚动分：一圈收分归赢墩者，接风不清算继续滚
}

// Game 一桌牌局
type Game struct {
	seats      [8]Seat
	trick      Trick
	phase      Phase
	ratio      int
	turn       int // 轮到谁（contextPosID）；-1 未设置
	finalScore int // 终局胜方最终得分（全队出完时含带走对方未出手分牌）
	winner     []int
	loser      []int
	rnd        func(n int) int // 返回 [0,n) 随机整数；测试可注入
}

// New 创建一局
func New() *Game {
	g := &Game{rnd: defaultRnd}
	g.Init()
	return g
}

func defaultRnd(n int) int { return rand.Intn(n) }

// Init 重置对局（复用 Game 对象时调用）
func (g *Game) Init() {
	for i := range g.seats {
		g.seats[i] = Seat{PosID: i, Called: -1}
	}
	g.trick = Trick{Pos: -1}
	g.phase = PhaseIdle
	g.ratio = 1
	g.turn = -1
	g.finalScore = 0
	g.winner = nil
	g.loser = nil
}

// Start 发牌并确定先手，进入叫分阶段
func (g *Game) Start() {
	g.phase = PhaseCall
	g.deal()
	g.whoFirst()
}

// deal 324 张随机发 8 家各 40 张，剩余 4 张按 last4 随机补给
func (g *Game) deal() {
	deck := card.NewDeck()
	maxIndex := len(deck) - 1
	c1, c2, c3, c4 := g.rnd(2), g.rnd(2), g.rnd(2), g.rnd(2)
	// 324张牌剩余4张：0,2随机多分一张，1和3随机分一张，4,6随机多分一张，5,7随机分一张
	last4 := [8]int{c1, c2, 1 - c1, 1 - c2, c3, c4, 1 - c3, 1 - c4}

	for i := range g.seats {
		hand := make([]card.Card, 0, 41)
		draw := func() {
			offset := g.rnd(maxIndex + 1)
			hand = append(hand, deck[offset])
			deck = append(deck[:offset], deck[offset+1:]...)
			maxIndex--
		}
		for j := 0; j < 40; j++ {
			draw()
		}
		if last4[i] == 1 {
			draw()
		}
		sort.SliceStable(hand, func(a, b int) bool { return hand[a].Face < hand[b].Face })
		g.seats[i].Hand = hand
	}
}

// whoFirst 红桃 3 最多者先出；相同则比 3 的总数（不分花色）；仍相同随机定先手。
// 先手自动叫满 3 分，直接获得首出权。
func (g *Game) whoFirst() {
	best := []int{0}
	maxH3, maxT3 := -1, -1
	for i := range g.seats {
		h3, t3 := 0, 0
		for _, c := range g.seats[i].Hand {
			if c.Face == card.Face3 {
				t3++
				if c.Suit == card.Heart {
					h3++
				}
			}
		}
		if h3 > maxH3 || (h3 == maxH3 && t3 > maxT3) {
			best = []int{i}
			maxH3, maxT3 = h3, t3
		} else if h3 == maxH3 && t3 == maxT3 {
			best = append(best, i)
		}
	}
	g.turn = best[g.rnd(len(best))]
	g.seats[g.turn].Called = 3
}

// ---------- 规则链（共享上下文、责任链式校验） ----------

// PlayContext 一次出牌尝试的共享上下文，规则链逐环读写
type PlayContext struct {
	Game   *Game
	Pos    int
	Cards  []card.Card
	Shapes []card.Shape // ruleClassify 产出的全部合法解读
	Hit    *card.Shape  // 命中（可用于落桌）的解读
}

// Violation 规则未通过
type Violation struct {
	Rule   string
	Reason string
}

func (v *Violation) Error() string { return v.Rule + ": " + v.Reason }

// Rule 一条校验规则：通过返回 nil
type Rule func(*PlayContext) *Violation

// playRules 出牌校验链。注意与旧实现一致：不校验轮次——
// 越序由 Play 应用阶段检查（旧 hub 先 Validate 后 Next 的语义）。
var playRules = []Rule{rulePhasePlaying, ruleHandHas, ruleClassify, ruleBeatTable}

func rulePhasePlaying(ctx *PlayContext) *Violation {
	if ctx.Game.phase != PhasePlaying {
		return &Violation{"phase", "非出牌阶段"}
	}
	return nil
}

// ruleHandHas 防作弊：所出的牌确实在手。与旧实现逐条一致——
// 每张牌独立查手牌（重复出同一张具体牌不会被拦，bug-for-bug 保留）。
func ruleHandHas(ctx *PlayContext) *Violation {
	hand := ctx.Game.seats[ctx.Pos].Hand
	for _, c := range ctx.Cards {
		found := false
		for _, h := range hand {
			if h == c {
				found = true
				break
			}
		}
		if !found {
			return &Violation{"hand", "所出牌不在手上"}
		}
	}
	return nil
}

func ruleClassify(ctx *PlayContext) *Violation {
	ctx.Shapes = card.Classify(ctx.Cards)
	if len(ctx.Shapes) == 0 {
		return &Violation{"shape", "构不成合法牌型"}
	}
	return nil
}

// ruleBeatTable 压制判定：新一轮首出（自己是上一手出牌人）任意合法牌型，
// 取基础解读；否则按解读顺序尝试压桌面牌
func ruleBeatTable(ctx *PlayContext) *Violation {
	g := ctx.Game
	if g.trick.Pos == ctx.Pos || g.trick.Pos == -1 {
		ctx.Hit = &ctx.Shapes[0]
		return nil
	}
	for i := range ctx.Shapes {
		if ctx.Shapes[i].Beats(g.trick.Top) {
			ctx.Hit = &ctx.Shapes[i]
			return nil
		}
	}
	return &Violation{"beat", "压不住桌面牌"}
}

// ---------- 用例方法 ----------

// PlayResult 一手牌的受理结果：
//   - Accepted=false：牌不合法（旧 hub 发 PLAY_CARD_ERROR(data)，状态不变）
//   - Accepted=true, Applied=false：越序，对局被置 PhaseError（旧 Next 毒化语义）
//   - Accepted=true, Applied=true：正常受理（出牌或过牌）
type PlayResult struct {
	Accepted bool
	Applied  bool
	HasShape bool       // 是否命中牌型（过牌/未受理为 false；越序但合法时为 true）
	Shape    card.Shape // 命中牌型（HasShape 时有效；wire 的 key/type 取 Rank/TypeName）
}

// Play 受理一手出牌（空手牌 = 过牌，跳过校验直接进入应用，与旧 hub 一致）
func (g *Game) Play(pos int, cards []card.Card) PlayResult {
	res := PlayResult{Accepted: len(cards) == 0}
	if !res.Accepted {
		ctx := g.validatePlay(pos, cards)
		if ctx == nil {
			return res // 未受理，状态不变
		}
		res.Accepted = true
		res.HasShape, res.Shape = true, *ctx.Hit
	}
	return g.apply(pos, cards, res)
}

// validatePlay 规则链校验（不含轮次）；不通过返回 nil
func (g *Game) validatePlay(pos int, cards []card.Card) *PlayContext {
	ctx := &PlayContext{Game: g, Pos: pos, Cards: cards}
	for _, rule := range playRules {
		if v := rule(ctx); v != nil {
			return nil
		}
	}
	return ctx
}

// CallScore 叫分推进。载荷 score 与旧实现一致不被采用；
// 语义等同旧 Next(pos, nil)：出牌阶段的 CALL_SCORE 等同过牌（不可达路径，保留）。
func (g *Game) CallScore(pos int) PlayResult {
	return g.apply(pos, nil, PlayResult{Accepted: true})
}

// apply 应用一口（Next 语义）：越序先毒化；叫分阶段按最高叫分定首出；
// 出牌阶段推进轮转/桌面/收分/接风/终局
func (g *Game) apply(pos int, cards []card.Card, res PlayResult) PlayResult {
	if pos != g.turn {
		// 一般不会进来：越序出牌毒化对局，客户端必须严格按 CTX_PLAY_CHANGE 出牌
		g.phase = PhaseError
		return res
	}
	if g.phase == PhaseCall {
		if score, scorePos := g.maxScoreInfo(); score > 0 {
			g.phase = PhasePlaying
			g.turn = scorePos
			g.trick.Pos = scorePos
		} else {
			// 需要重新发牌，找不到先出牌的人
			g.phase = PhaseRedeal
		}
		return res
	}
	if g.phase == PhasePlaying {
		res.Applied = true

		// 先推进轮转（出完牌的人要跳过）
		g.turn = g.nextPosID(pos)

		if res.HasShape {
			g.trick.Pos = pos
			g.trick.Top = res.Shape
		}
		g.removeCards(cards, pos)
		g.trick.Pot += scoreOf(cards)

		// 又轮回到上一手出牌人 => 一圈无人压牌，桌面分归其收取
		if g.trick.Pos == g.turn {
			g.seats[g.trick.Pos].Captured += g.trick.Pot
			g.trick.Pot = 0
		}

		// 出完牌则让同队下一位有牌的队友接风（本轮余家不再有机会压）
		if g.seats[pos].Out() {
			g.turn = g.nextGroupPosID(g.trick.Pos)
			g.trick.Pos = g.turn
		}

		if g.isGameOver() {
			g.phase = PhaseOver
		}
	}
	return res
}

// scoreOf 出牌分：5 计 5 分，10/K 计 10 分
func scoreOf(cards []card.Card) int {
	sum := 0
	for _, c := range cards {
		sum += c.Score()
	}
	return sum
}

// removeCards 出牌后从手牌移除（按 face+suit 匹配，逐张移一次）
func (g *Game) removeCards(cards []card.Card, pos int) {
	hand := g.seats[pos].Hand
	for _, c := range cards {
		for i, cur := range hand {
			if cur == c {
				hand = append(hand[:i], hand[i+1:]...)
				break
			}
		}
	}
	g.seats[pos].Hand = hand
}

// nextPosID 顺时针下一个还有牌的人；无人有牌返回 -1（JS 返回 undefined）
func (g *Game) nextPosID(pos int) int {
	for i := 0; i < 7; i++ {
		next := (pos + 1 + i) % 8
		if !g.seats[next].Out() {
			return next
		}
	}
	return -1
}

// nextGroupPosID 出完牌后让同队下一位有牌的队友接手（隔位顺延）
func (g *Game) nextGroupPosID(pos int) int {
	for i := 0; i < 3; i++ {
		next := (pos + 2 + 2*i) % 8
		if !g.seats[next].Out() {
			return next
		}
	}
	return -1
}

// isGameOver 终局判定：某队全部出完（带走对方未出手分牌），或出完者累计抓分 ≥300。
func (g *Game) isGameOver() bool {
	even := [4]int{0, 2, 4, 6}
	odd := [4]int{1, 3, 5, 7}
	if g.teamAllOut(even) {
		g.setWinner(even, odd)
		g.finalScore = g.teamCaptured(even) + g.leftoverScore(odd)
		return true
	}
	if g.teamAllOut(odd) {
		g.setWinner(odd, even)
		g.finalScore = g.teamCaptured(odd) + g.leftoverScore(even)
		return true
	}
	evenSum, oddSum := 0, 0
	for i := range g.seats {
		if g.seats[i].Out() {
			if g.seats[i].Team() == 0 {
				evenSum += g.seats[i].Captured
			} else {
				oddSum += g.seats[i].Captured
			}
		}
	}
	if evenSum >= 300 {
		g.setWinner(even, odd)
		g.finalScore = evenSum
		return true
	}
	if oddSum >= 300 {
		g.setWinner(odd, even)
		g.finalScore = oddSum
		return true
	}
	return false
}

func (g *Game) teamAllOut(team [4]int) bool {
	for _, p := range team {
		if !g.seats[p].Out() {
			return false
		}
	}
	return true
}

func (g *Game) setWinner(winner, loser [4]int) {
	g.winner = []int{winner[0], winner[1], winner[2], winner[3]}
	g.loser = []int{loser[0], loser[1], loser[2], loser[3]}
}

// teamCaptured 某队已收分合计
func (g *Game) teamCaptured(team [4]int) int {
	s := 0
	for _, p := range team {
		s += g.seats[p].Captured
	}
	return s
}

// leftoverScore 一方剩余手牌中的分牌合计（全队出完终局时被对方带走）
func (g *Game) leftoverScore(team [4]int) int {
	s := 0
	for _, p := range team {
		for _, c := range g.seats[p].Hand {
			s += c.Score()
		}
	}
	return s
}

// maxScoreInfo 当前最高叫分（whoFirst 已把先手置 3）
func (g *Game) maxScoreInfo() (score, posID int) {
	score, posID = 0, 0
	for i := range g.seats {
		if g.seats[i].Called > score {
			score = g.seats[i].Called
			posID = i
		}
	}
	return
}

// ---------- 快照（hub 组装 wire 帧 / 重连重放 / 落库用） ----------

func (g *Game) Phase() Phase  { return g.phase }
func (g *Game) Turn() int     { return g.turn }
func (g *Game) TrickPos() int { return g.trick.Pos }
func (g *Game) TmpFeng() int  { return g.trick.Pot }

// TrickTop 当前一轮需要压制的牌型；ok=false 表示自由首出（无需压牌）。
// 判断与 validatePlay 一致：无有效上手（Pos==-1）或一圈压回出牌人本人（Pos==轮到者）均为首出。
func (g *Game) TrickTop() (top card.Shape, ok bool) {
	if g.trick.Pos == -1 || g.trick.Pos == g.turn {
		return card.Shape{}, false
	}
	return g.trick.Top, true
}

// CtxScore 叫分上下文，恒 {1,2,3}（旧 contextScore 从未变更）
func (g *Game) CtxScore() [3]int { return [3]int{1, 2, 3} }

// posArray8 内部快照用数组承载（原 JS 数字键对象改为 wire 输出端重建 {"0":..} 形态）
func posArray8(f func(i int) int) [8]int {
	var a [8]int
	for i := range a {
		a[i] = f(i)
	}
	return a
}

// CalledScores 各座位叫分快照
func (g *Game) CalledScores() [8]int {
	return posArray8(func(i int) int { return g.seats[i].Called })
}

// SumFeng 各座位累计抓分快照
func (g *Game) SumFeng() [8]int {
	return posArray8(func(i int) int { return g.seats[i].Captured })
}

// Hands 8 家手牌快照（旧实现将 8 家手牌全部广播，前端亮牌/余张依赖；
// 红桃统计等 wire 细节由 hub 侧组装）
func (g *Game) Hands() []HandSnapshot {
	out := make([]HandSnapshot, len(g.seats))
	for i := range g.seats {
		cards := g.seats[i].Hand
		if cards == nil {
			cards = []card.Card{}
		}
		out[i] = HandSnapshot{PosID: i, Cards: cards}
	}
	return out
}

// HandLen 某座位剩余手牌数（断线重连/落库）
func (g *Game) HandLen(pos int) int { return len(g.seats[pos].Hand) }

// DizhuPosID 首出者（最高叫分者）
func (g *Game) DizhuPosID() int {
	_, posID := g.maxScoreInfo()
	return posID
}

// TopCards 无底牌玩法，恒为空（wire 契约）
func (g *Game) TopCards() []card.Card { return []card.Card{} }

// Result 终局结果（GAME_OVER payload）
func (g *Game) Result() Result {
	return Result{Winner: g.winner, Loser: g.loser, Score: g.finalScore, Ratio: g.ratio}
}

// Result 终局结果（纯领域值；wire 字段名由 hub 侧 payload 承载）
type Result struct {
	Winner []int
	Loser  []int
	Score  int
	Ratio  int
}
