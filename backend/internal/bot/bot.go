// Package bot bot/trustee play strategy; kept in sync with e2e-audit.js
// candidates() and the parser.js hints.
package bot

import (
	"math/rand"
	"sort"

	"talkcards/backend/internal/card"
)

// Candidates groups each face by its current count (1=single 2=pair 3=triple,
// ≥4 cards or ≥3 same joker = bomb) and returns candidates in group order.
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

	buckets := [4][][]card.Card{} // single/pair/triple/bomb
	for _, f := range faces {
		cs := byFace[f]
		gi := len(cs) - 1
		if f.IsJoker() && len(cs) >= 3 {
			gi = 3 // ≥3 same joker counts as a king bomb, not a triple
		}
		if gi > 3 {
			gi = 3
		}
		buckets[gi] = append(buckets[gi], cs)
	}
	// Bombs: fewer cards first, then lower face.
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

// Lead free lead (first play of a trick): small cards (≤8, any of single/pair/
// triple) go first by ascending face; once small cards are gone, play whole
// groups in group order (single → pair → triple → bomb).
func Lead(hand []card.Card) []card.Card {
	cands := Candidates(hand)
	best := -1
	for i, cs := range cands {
		if len(cs) > 3 {
			break // only bombs remain; they don't take part in small-card priority
		}
		if cs[0].Face <= smallTopRankMax && (best == -1 || cs[0].Face < cands[best][0].Face) {
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

// Follow plays the first whole group that beats top; nil means pass.
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

// FollowCtx context for smart following.
type FollowCtx struct {
	Hand          []card.Card
	Top           card.Shape     // current effective shape on the table
	TopIsTeammate bool           // whether the last play came from a teammate (same-parity seats)
	Pot           int            // rolling pot score (TmpFeng)
	Rand          func() float64 // [0,1) source; nil uses math/rand global (injectable for tests)
}

// FollowSmart smart follow (not leading):
//  1. Never take teammate cards unless they are a ≤8 single/pair/triple;
//  2. When no whole regular group beats an opponent's single, may split a
//     pair of A/2/jokers and follow with one card (keep the other in hand);
//  3. Only a bomb can beat and pot is empty: 4 cards ≤8 → 20%, 4 big-face →
//     10%, >4 cards (incl. king bombs) → 5% chance to play it;
//  4. Pot has score ≤50: 4 cards → 80%, 5 → 40%, ≥6 (incl. king bombs) → 10%;
//     pot >50 always plays.
//
// Regular (non-bomb) follows are not gated by the dice roll; nil means pass.
// Probability tiers live in static.go.
func FollowSmart(ctx FollowCtx) []card.Card {
	if ctx.TopIsTeammate && !isSmallTop(ctx.Top) {
		return nil
	}

	// Scan candidates in group order: first beating non-bomb = smallest regular
	// follow; first beating bomb = smallest bomb.
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
	if sp := splitHighPair(ctx.Hand, ctx.Top); sp != nil {
		return sp
	}
	if bomb == nil {
		return nil
	}

	rng := ctx.Rand
	if rng == nil {
		rng = rand.Float64
	}
	if rng() < bombChance(bombShape, ctx.Pot) {
		return bomb
	}
	return nil
}

// isSmallTop reports whether a teammate's play is a ≤8 single/pair/triple
// (small enough to let a teammate take it and gain the lead).
func isSmallTop(top card.Shape) bool {
	switch top.Kind {
	case card.KindSingle, card.KindPair, card.KindTriple:
		return top.Rank <= smallTopRankMax
	}
	return false
}

// splitHighPair: with exactly a pair of A/2/jokers in hand, an opponent's
// single on the table and no whole regular group that beats it, split one card
// off the pair to follow (face must beat the table). The other card stays in
// hand. Only pairs split: triples/bombs (incl. 3-joker king bombs) never do.
func splitHighPair(hand []card.Card, top card.Shape) []card.Card {
	if top.Kind != card.KindSingle {
		return nil
	}
	byFace := make(map[card.Face][]card.Card)
	for _, c := range hand {
		byFace[c.Face] = append(byFace[c.Face], c)
	}
	faces := make([]card.Face, 0, len(byFace))
	for f := range byFace {
		faces = append(faces, f)
	}
	sort.Slice(faces, func(i, j int) bool { return faces[i] < faces[j] })
	for _, f := range faces {
		if f < card.FaceA { // only high pairs: A/2/small joker/big joker
			continue
		}
		cs := byFace[f]
		if len(cs) != 2 {
			continue
		}
		if int(f) > top.Rank {
			return cs[:1]
		}
	}
	return nil
}

// bombChance returns the play probability for each bomb when nothing else can
// beat the table (tier constants in static.go).
func bombChance(b card.Shape, pot int) float64 {
	if pot > 50 {
		return 1 // pot over 50: must beat
	}
	king := b.Kind == card.KindKingBomb
	if pot == 0 {
		switch {
		case king || b.Len > 4:
			return bombPot0Big
		case b.Rank <= 8:
			return bombPot0SmallRank
		default:
			return bombPot0BigRank
		}
	}
	switch {
	case king || b.Len >= 6:
		return bombPot50Len6P
	case b.Len == 5:
		return bombPot50Len5
	default:
		return bombPot50Len4
	}
}
