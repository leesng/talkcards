// card.go card domain model: Face/Suit and the wire encoding {"value":3-17,"type":0-3}.
package card

import (
	"encoding/json"
	"fmt"
)

// Face underlying value is the wire "value": 3-13 normal, 14=A, 15=2, 16=small joker, 17=big joker.
// Values compare as strength order (3 < … < A < 2 < jo < JO).
type Face int16

const (
	Face3  Face = 3
	Face4  Face = 4
	Face5  Face = 5
	Face6  Face = 6
	Face7  Face = 7
	Face8  Face = 8
	Face9  Face = 9
	Face10 Face = 10
	FaceJ  Face = 11
	FaceQ  Face = 12
	FaceK  Face = 13
	FaceA  Face = 14
	Face2  Face = 15
	Jo     Face = 16 // small joker
	JO     Face = 17 // big joker
)

func (f Face) String() string {
	if s, ok := faceNames[f]; ok {
		return s
	}
	return fmt.Sprintf("%d", int(f))
}

func (f Face) IsJoker() bool { return f == Jo || f == JO }

// Suit underlying value is the wire "type": 0♥ 1♦ 2♠ 3♣. Jokers have no suit
// (always 0, matching the legacy encoding).
type Suit int16

const (
	Heart   Suit = 0
	Diamond Suit = 1
	Spade   Suit = 2
	Club    Suit = 3
)

func (s Suit) String() string { return suitNames[s] }

// Card: same-face cards across the 6 decks are fully interchangeable; no instance identity.
type Card struct {
	Face Face
	Suit Suit
}

// Ordinal returns the 1-15 human-readable rank order.
func (c Card) Ordinal() int { return int(c.Face) - 2 }

func (c Card) Score() int { return scoreTable[c.Face] }

func (c Card) IsJoker() bool { return c.Face.IsJoker() }

func (c Card) String() string {
	if c.IsJoker() {
		return c.Face.String()
	}
	return c.Face.String() + c.Suit.String()
}

// wireCard frontend card encoding: value 3-13 normal, 14=A, 15=2, 16=small joker, 17=big joker;
// type 0=hearts 1=diamonds 2=spades 3=clubs.
type wireCard struct {
	Value int `json:"value"`
	Type  int `json:"type"`
}

func (c Card) MarshalJSON() ([]byte, error) {
	return json.Marshal(wireCard{Value: int(c.Face), Type: int(c.Suit)})
}

func (c *Card) UnmarshalJSON(b []byte) error {
	var w wireCard
	if err := json.Unmarshal(b, &w); err != nil {
		return err
	}
	c.Face, c.Suit = Face(w.Value), Suit(w.Type)
	return nil
}

// NewDeck builds 6 decks × 54 = 324 cards. Jokers use type=0, so they count
// toward the hearts statistics — matches the legacy deal.
func NewDeck() []Card {
	deck := make([]Card, 0, 324)
	for i := 0; i < 6; i++ {
		for v := Face3; v <= Face2; v++ {
			for s := Heart; s <= Club; s++ {
				deck = append(deck, Card{Face: v, Suit: s})
			}
		}
		deck = append(deck, Card{Face: Jo, Suit: Heart}, Card{Face: JO, Suit: Heart})
	}
	return deck
}
