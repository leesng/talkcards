// Package game is the per-table game aggregate: dealing, first-player
// selection, rule-chain validation, turn rotation, scoring, wind relay and
// game-over. Pure domain, no I/O; not concurrency-safe (guarded by the hub
// lock). Wire frames are assembled by hub.
package game

import (
	"math/rand"
	"sort"

	"talkcards/backend/internal/card"
)

// Phase values mirror the legacy numeric status codes 0-5.
type Phase int

const (
	PhaseIdle    Phase = 0
	PhasePlaying Phase = 2
	PhaseOver    Phase = 3
	PhaseError   Phase = 5
)

// HandSnapshot is a domain snapshot of one player's hand. The wire shape
// (id/cards/ht with HT heart stats) is assembled by hub/translator; this
// package knows nothing about JSON field names.
type HandSnapshot struct {
	PosID int
	Cards []card.Card
}

type Seat struct {
	PosID    int
	Hand     []card.Card
	Captured int // captured points; only players who are out count toward the 300 line
}

// Team: odd posIDs (1/3/5/7) vs even posIDs (0/2/4/6).
func (s *Seat) Team() int { return s.PosID % 2 }

func (s *Seat) Out() bool { return len(s.Hand) == 0 }

// Trick is the table state: current winning play and rolling points.
// Pos is the seat owning the winning play, or -1 when there is none
// (unreachable once playing starts — there is always an owner).
type Trick struct {
	Pos int
	Top card.Shape // winning shape (stale after wind relay; Pos matching takes priority over Top)
	Pot int        // rolling table points; collected by the trick winner, kept rolling across wind relays
}

type Game struct {
	seats      [8]Seat
	trick      Trick
	phase      Phase
	ratio      int
	turn       int // current player (contextPosID); -1 unset
	leader     int // first player of the game, used for SHOW_TOP_CARD on start/reconnect
	finalScore int // winner's final score; on full-team-out includes the losers' unplayed point cards
	winner     []int
	loser      []int
	rnd        func(n int) int // returns [0,n); injectable for tests
}

func New() *Game {
	g := &Game{rnd: defaultRnd}
	g.Init()
	return g
}

func defaultRnd(n int) int { return rand.Intn(n) }

// Init resets the game (used when reusing a Game object).
func (g *Game) Init() {
	for i := range g.seats {
		g.seats[i] = Seat{PosID: i}
	}
	g.trick = Trick{Pos: -1}
	g.phase = PhaseIdle
	g.ratio = 1
	g.turn = -1
	g.leader = -1
	g.finalScore = 0
	g.winner = nil
	g.loser = nil
}

// Snapshot deep-copies the state for the recovery mechanism.
func (g *Game) Snapshot() *Game {
	c := *g
	for i := range g.seats {
		c.seats[i].Hand = append([]card.Card(nil), g.seats[i].Hand...)
	}
	c.winner = append([]int(nil), g.winner...)
	c.loser = append([]int(nil), g.loser...)
	return &c
}

// Restore rolls back all state from a snapshot.
func (g *Game) Restore(snap *Game) {
	s := *snap
	for i := range snap.seats {
		s.seats[i].Hand = append([]card.Card(nil), snap.seats[i].Hand...)
	}
	s.winner = append([]int(nil), snap.winner...)
	s.loser = append([]int(nil), snap.loser...)
	*g = s
}

// Start deals and picks the first player, entering the playing phase
// directly — the bidding phase was deliberately removed; the leader leads.
func (g *Game) Start() {
	g.phase = PhasePlaying
	g.deal()
	g.whoFirst()
	g.trick.Pos = g.turn
}

// deal distributes the 324-card deck: 40 cards per seat, plus the 4 leftover
// cards dealt one each to the seats picked by last4.
func (g *Game) deal() {
	deck := card.NewDeck()
	maxIndex := len(deck) - 1
	c1, c2, c3, c4 := g.rnd(2), g.rnd(2), g.rnd(2), g.rnd(2)
	// leftover 4 cards: one extra each to two of {0,1}/{2,3} and two of {4,5}/{6,7}
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

// whoFirst: most heart-3s leads; ties broken by total count of 3s (any suit),
// then randomly. Deliberate deviation from the Node version, which used a
// heart-suit dictionary order that included jokers.
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
	g.leader = g.turn
}

// PlayContext is the shared context of one play attempt, read and written by
// each rule in the chain.
type PlayContext struct {
	Game   *Game
	Pos    int
	Cards  []card.Card
	Shapes []card.Shape // all legal interpretations produced by ruleClassify
	Hit    *card.Shape  // the interpretation that can hit the table
}

type Violation struct {
	Rule   string
	Reason string
}

func (v *Violation) Error() string { return v.Rule + ": " + v.Reason }

// Rule is one validation step; nil means passed.
type Rule func(*PlayContext) *Violation

var playRules = []Rule{rulePhasePlaying, ruleHandHas, ruleClassify, ruleBeatTable}

func rulePhasePlaying(ctx *PlayContext) *Violation {
	if ctx.Game.phase != PhasePlaying {
		return &Violation{"phase", "非出牌阶段"}
	}
	return nil
}

// ruleHandHas checks the played cards are actually in hand. Bug-for-bug with
// the legacy implementation: each card is checked against the hand
// independently, so playing the same exact card twice is not caught.
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

// ruleBeatTable: when leading a new round (trick.Pos is self or unset) any
// legal shape is fine and the base interpretation is used; otherwise the
// interpretations are tried in order against the table's top shape.
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

// PlayResult is the outcome of one play attempt:
//   - Accepted=false: rejected (illegal cards or out of turn), no state change
//   - Accepted=true, Applied=true: applied normally (play or pass)
type PlayResult struct {
	Accepted bool
	Applied  bool
	HasShape bool       // hit a shape (false on pass/reject)
	Shape    card.Shape // the hit shape, valid when HasShape; wire key/type come from Rank/TypeName
	Rejected *Violation // set when Accepted=false; Rule=="turn" marks out-of-turn
}

// Play handles one play attempt. An empty hand means pass: validation is
// skipped and it goes straight to apply, matching the legacy hub.
func (g *Game) Play(pos int, cards []card.Card) PlayResult {
	// Out of turn: always rejected with no state change (intentional drop of
	// the legacy poison semantics; triggers reproduced in scripts/poison-repro.js).
	if pos != g.turn {
		return PlayResult{Rejected: &Violation{"turn", "还没轮到你出牌"}}
	}
	res := PlayResult{Accepted: len(cards) == 0}
	if !res.Accepted {
		ctx := g.validatePlay(pos, cards)
		if ctx == nil {
			return res // rejected, no state change
		}
		res.Accepted = true
		res.HasShape, res.Shape = true, *ctx.Hit
	}
	return g.apply(pos, cards, res)
}

func (g *Game) validatePlay(pos int, cards []card.Card) *PlayContext {
	ctx := &PlayContext{Game: g, Pos: pos, Cards: cards}
	for _, rule := range playRules {
		if v := rule(ctx); v != nil {
			return nil
		}
	}
	return ctx
}

// apply advances rotation/trick/scoring/wind relay/game-over.
func (g *Game) apply(pos int, cards []card.Card, res PlayResult) PlayResult {
	if g.phase == PhasePlaying {
		res.Applied = true

		// Advance rotation first (players who are out must be skipped).
		g.turn = g.nextPosID(pos)

		if res.HasShape {
			g.trick.Pos = pos
			g.trick.Top = res.Shape
		}
		g.removeCards(cards, pos)
		g.trick.Pot += scoreOf(cards)

		// Rotation wrapped back to the last player => nobody beat it; they collect the pot.
		if g.trick.Pos == g.turn {
			g.seats[g.trick.Pos].Captured += g.trick.Pot
			g.trick.Pot = 0
		}

		// On running out of cards, the next teammate with cards takes over
		// (wind relay); the other players don't get to beat this trick.
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

// scoreOf: 5s are worth 5 points, 10s and kings 10.
func scoreOf(cards []card.Card) int {
	sum := 0
	for _, c := range cards {
		sum += c.Score()
	}
	return sum
}

// removeCards removes played cards from the hand (matched by face+suit, one
// occurrence removed per played card).
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

// nextPosID returns the next player clockwise still holding cards, or -1 if
// nobody does (JS returned undefined).
func (g *Game) nextPosID(pos int) int {
	for i := 0; i < 7; i++ {
		next := (pos + 1 + i) % 8
		if !g.seats[next].Out() {
			return next
		}
	}
	return -1
}

// nextGroupPosID picks the next teammate with cards two seats at a time
// (wind-relay handover after a player runs out).
func (g *Game) nextGroupPosID(pos int) int {
	for i := 0; i < 3; i++ {
		next := (pos + 2 + 2*i) % 8
		if !g.seats[next].Out() {
			return next
		}
	}
	return -1
}

// isGameOver: a team all out (taking the opponents' unplayed point cards), or
// the players who are out on one team captured >= 300 in total.
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

func (g *Game) teamCaptured(team [4]int) int {
	s := 0
	for _, p := range team {
		s += g.seats[p].Captured
	}
	return s
}

// leftoverScore totals the point cards left in a team's hands (taken by the
// winners on a full-team-out game over).
func (g *Game) leftoverScore(team [4]int) int {
	s := 0
	for _, p := range team {
		for _, c := range g.seats[p].Hand {
			s += c.Score()
		}
	}
	return s
}

func (g *Game) Phase() Phase  { return g.phase }
func (g *Game) Turn() int     { return g.turn }
func (g *Game) TrickPos() int { return g.trick.Pos }
func (g *Game) TmpFeng() int  { return g.trick.Pot }

// TrickTop is the shape the current round must beat; ok=false means a free
// lead. The condition matches validatePlay: no winning play yet (Pos==-1) or
// the round wrapped back to the current player (Pos==turn) both mean leading.
func (g *Game) TrickTop() (top card.Shape, ok bool) {
	if g.trick.Pos == -1 || g.trick.Pos == g.turn {
		return card.Shape{}, false
	}
	return g.trick.Top, true
}

// posArray8 builds internal snapshots as arrays; the wire-side rebuilds the
// legacy JS numeric-key-object shape {"0":..} at output time.
func posArray8(f func(i int) int) [8]int {
	var a [8]int
	for i := range a {
		a[i] = f(i)
	}
	return a
}

func (g *Game) SumFeng() [8]int {
	return posArray8(func(i int) int { return g.seats[i].Captured })
}

// Hands broadcasts all 8 hands (bug-for-bug with the legacy implementation;
// the frontend relies on it for revealed hands and remaining counts). Wire
// details like heart stats are assembled on the hub side.
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

func (g *Game) HandLen(pos int) int { return len(g.seats[pos].Hand) }

func (g *Game) DizhuPosID() int { return g.leader }

// TopCards: no kitty in this variant; always empty (wire contract).
func (g *Game) TopCards() []card.Card { return []card.Card{} }

// Result is the game-over result (GAME_OVER payload).
func (g *Game) Result() Result {
	return Result{Winner: g.winner, Loser: g.loser, Score: g.finalScore, Ratio: g.ratio}
}

// Result is a pure domain value; wire field names live in the hub payload.
type Result struct {
	Winner []int
	Loser  []int
	Score  int
	Ratio  int
}
