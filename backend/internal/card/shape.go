// shape.go 牌型识别与压制比较（规则引擎的"比较器"半边，纯函数）。
// 行为与旧 card.Validate + game.Validate 内嵌压牌逻辑逐条对齐（bug-for-bug）。
package card

import "fmt"

// Kind 牌型（只有五种，无顺子/连对/带牌）
type Kind uint8

const (
	KindSingle   Kind = iota // 单张
	KindPair                 // 对子
	KindTriple               // 三条
	KindBomb                 // 炸弹：4-6 张同值
	KindKingBomb             // 王炸：3-6 张完全相同的王（特殊炸弹）
)

func (k Kind) String() string {
	return []string{"Single", "Pair", "Triple", "Bomb", "KingBomb"}[k]
}

// Shape 一次出牌的一个合法牌型解读。Rank 为牌面（3-17），Len 为张数。
// 同一组牌可有多个解读（如 4 小王 = 炸弹 + 王炸），由调用方逐个尝试压制。
type Shape struct {
	Kind Kind
	Rank int
	Len  int
}

// TypeName 复现旧牌型名（wire CTX_PLAY_CHANGE 的 type 字段逐字依赖）：
// "A"/"AA"/"AAA"/"AAAA"/"AAAA_n"，王炸 "XKINGn"/"DKINGn"。
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

// Classify 识别一组出牌的全部合法牌型解读，顺序与旧 Validate 的 Types 一致
// （基础牌型在前，王炸在后；命中即由调用方按序尝试压制）。
// 合法张数 1-24 且全同面；否则无解读。
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

// Beats 候选牌型能否压住桌面牌型（game-rules.md 压牌矩阵）：
//   - 王炸对王炸：张数多者大；同张数大王 > 小王
//   - 王炸对非王炸：可压张数 ≤ 2N-1 的任意一手
//   - 普通炸弹反压王炸：张数须 > 2N-1
//   - 炸弹压一切非炸弹；炸弹之间张数多者大，同张数比牌面
//   - 其余：同牌型同张数比牌面
//
// 王的多重集不走普通炸长度规则（旧实现以 key!=16/17 排除，此处保留；
// 6 副牌下 7+ 同值不可达，该分支实际不可触达）。
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
