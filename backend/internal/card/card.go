// card.go 牌领域模型：Face/Suit 与 wire 编码 {"value":3-17,"type":0-3} 的转换收敛于此。
package card

import (
	"encoding/json"
	"fmt"
)

// Face 牌面，底层数值即 wire 的 value（3-13 普通牌，14=A，15=2，16=小王，17=大王），
// 天然可比较即大小序（3 < 4 < … < A < 2 < jo < JO）。Ordinal() 给出 1-15 的人读序。
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
	Jo     Face = 16 // 小王
	JO     Face = 17 // 大王
)

func (f Face) String() string {
	if s, ok := faceNames[f]; ok {
		return s
	}
	return fmt.Sprintf("%d", int(f))
}

// IsJoker 是否为王（16/17）
func (f Face) IsJoker() bool { return f == Jo || f == JO }

// Suit 花色，底层即 wire 的 type：A♥=0 B♦=1 C♠=2 D♣=3；王无花色（恒 0，与旧编码一致）
type Suit int16

const (
	Heart   Suit = 0 // A
	Diamond Suit = 1 // B
	Spade   Suit = 2 // C
	Club    Suit = 3 // D
)

func (s Suit) String() string { return suitNames[s] }

// Card 一张牌。同面牌跨副完全可互换（6 副 × 54），无实例身份。
type Card struct {
	Face Face
	Suit Suit
}

// Ordinal 牌面大小序 1-15（3=1 … 2=13，jo=14，JO=15）
func (c Card) Ordinal() int { return int(c.Face) - 2 }

// Score 分牌分值：5 计 5 分，10/K 计 10 分，其余 0
func (c Card) Score() int { return scoreTable[c.Face] }

// IsJoker 是否为王
func (c Card) IsJoker() bool { return c.Face.IsJoker() }

func (c Card) String() string {
	if c.IsJoker() {
		return c.Face.String()
	}
	return c.Face.String() + c.Suit.String()
}

// wireCard 与前端约定的牌编码：value 3-13 普通牌 14=A 15=2 16=小王 17=大王；type 0红桃 1方块 2黑桃 3草花
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

// NewDeck 6 副 × 54 = 324 张（大小王 type=0，计入红桃统计，与旧发牌一致）
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
