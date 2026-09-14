// shape.go shape classification and beating comparison (pure functions).
// Behavior is bug-for-bug aligned with the legacy card.Validate + game.Validate logic.
package card

import "fmt"

// Kind shape kinds (only five; no straights/combos in this game).
type Kind uint8

const (
	KindSingle   Kind = iota // single
	KindPair                 // pair
	KindTriple               // triple
	KindBomb                 // bomb: 4-6 cards of the same face
	KindKingBomb             // king bomb: 3-6 identical jokers (special bomb)
)

func (k Kind) String() string {
	return []string{"Single", "Pair", "Triple", "Bomb", "KingBomb"}[k]
}

// Shape one legal interpretation of a play. Rank is the face (3-17), Len the card count.
// The same cards can have multiple interpretations (e.g. 4 small jokers = bomb + king bomb);
// callers try each in order to beat the table.
type Shape struct {
	Kind Kind
	Rank int
	Len  int
}

// TypeName reproduces the legacy shape names — the wire CTX_PLAY_CHANGE "type"
// field depends on these exact strings: "A"/"AA"/"AAA"/"AAAA"/"AAAA_n",
// king bombs "XKINGn"/"DKINGn".
func (s Shape) TypeName() string {
	switch s.Kind {
	case KindSingle:
		return "A"
	case KindPair:
		return "AA"
	case KindTriple:
		return "AAA"
	case KindBomb:
		if s.Len == 4 {
			return "AAAA"
		}
		return fmt.Sprintf("AAAA_%d", s.Len)
	default: // KindKingBomb
		if s.Rank == int(Jo) {
			return fmt.Sprintf("XKING%d", s.Len)
		}
		return fmt.Sprintf("DKING%d", s.Len)
	}
}

// Classify returns all legal interpretations, ordered like the legacy Validate
// "Types" field: base shapes first, king bomb last. Valid sizes are 1-24 all
// same face; anything else yields no interpretation.
func Classify(cards []Card) []Shape {
	n := len(cards)
	if n < 1 || n > 24 {
		return nil
	}
	for _, c := range cards {
		if c.Face != cards[0].Face {
			return nil
		}
	}
	rank := int(cards[0].Face)

	var shapes []Shape
	switch n {
	case 1:
		shapes = append(shapes, Shape{KindSingle, rank, 1})
	case 2:
		shapes = append(shapes, Shape{KindPair, rank, 2})
	case 3:
		shapes = append(shapes, Shape{KindTriple, rank, 3})
	default:
		shapes = append(shapes, Shape{KindBomb, rank, n})
	}
	if kingBombLens[n] && cards[0].Face.IsJoker() {
		shapes = append(shapes, Shape{KindKingBomb, rank, n})
	}
	return shapes
}

// Beats reports whether the candidate shape beats the table shape (game-rules.md matrix):
//   - king bomb vs king bomb: more cards wins; equal count: big joker > small joker
//   - king bomb vs anything: beats any play with len ≤ 2N-1
//   - normal bomb beating a king bomb: needs len > 2N-1
//   - bomb beats every non-bomb; bomb vs bomb: more cards wins, equal count by face
//   - otherwise: same kind and same length, higher face wins
//
// Joker multisets intentionally skip the normal bomb length rules (kept from the
// legacy key!=16/17 exclusion); 7+ same-face jokers are unreachable with 6 decks.
func (c Shape) Beats(t Shape) bool {
	cKing, tKing := c.Kind == KindKingBomb, t.Kind == KindKingBomb
	switch {
	case cKing && tKing:
		return c.Len > t.Len || (c.Len == t.Len && c.Rank > t.Rank)
	case cKing:
		return t.Len <= 2*c.Len-1
	case tKing:
		return c.Kind == KindBomb && c.Rank != int(Jo) && c.Rank != int(JO) && c.Len > 2*t.Len-1
	case c.Kind == KindBomb && t.Kind == KindBomb:
		if c.Len == t.Len {
			return c.Rank > t.Rank
		}
		return c.Rank != int(Jo) && c.Rank != int(JO) && c.Len > t.Len
	case c.Kind == KindBomb:
		return c.Rank != int(Jo) && c.Rank != int(JO)
	case c.Kind == t.Kind && c.Len == t.Len:
		return c.Rank > t.Rank
	default:
		return false
	}
}
