package card

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"reflect"
	"testing"
)

func vals(vs ...int) []Card {
	cs := make([]Card, 0, len(vs))
	for i, v := range vs {
		cs = append(cs, Card{Face: Face(v), Suit: Suit(i % 4)})
	}
	return cs
}

// TestClassifyGolden frozen baseline (fixed after diffing against the deleted
// core-validator): same-face 1-24 cards only; base shapes before king bombs;
// king bombs only for 3-6 identical jokers; 7+ identical jokers get only the
// normal-bomb reading (unreachable, legacy semantics); face range unchecked.
func TestClassifyGolden(t *testing.T) {
	rep := func(n, v int) []int {
		vs := make([]int, n)
		for i := range vs {
			vs[i] = v
		}
		return vs
	}
	cases := []struct {
		name      string
		vals      []int
		want      []Shape
		wantNames []string
	}{
		{"空", nil, nil, nil},
		{"单张3", []int{3}, []Shape{{KindSingle, 3, 1}}, []string{"A"}},
		{"单张大王", []int{17}, []Shape{{KindSingle, 17, 1}}, []string{"A"}},
		{"对子", []int{8, 8}, []Shape{{KindPair, 8, 2}}, []string{"AA"}},
		{"杂牌两单", []int{8, 9}, nil, nil},
		{"对王小王", []int{16, 16}, []Shape{{KindPair, 16, 2}}, []string{"AA"}},
		{"大小王混", []int{16, 17}, nil, nil},
		{"三条", []int{13, 13, 13}, []Shape{{KindTriple, 13, 3}}, []string{"AAA"}},
		{"三小王双解读", []int{16, 16, 16}, []Shape{{KindTriple, 16, 3}, {KindKingBomb, 16, 3}}, []string{"AAA", "XKING3"}},
		{"三大王双解读", []int{17, 17, 17}, []Shape{{KindTriple, 17, 3}, {KindKingBomb, 17, 3}}, []string{"AAA", "DKING3"}},
		{"杂牌三带", []int{16, 16, 17}, nil, nil},
		{"四炸", []int{5, 5, 5, 5}, []Shape{{KindBomb, 5, 4}}, []string{"AAAA"}},
		{"四小王双解读", rep(4, 16), []Shape{{KindBomb, 16, 4}, {KindKingBomb, 16, 4}}, []string{"AAAA", "XKING4"}},
		{"四大王双解读", rep(4, 17), []Shape{{KindBomb, 17, 4}, {KindKingBomb, 17, 4}}, []string{"AAAA", "DKING4"}},
		{"五炸", rep(5, 9), []Shape{{KindBomb, 9, 5}}, []string{"AAAA_5"}},
		{"五小王双解读", rep(5, 16), []Shape{{KindBomb, 16, 5}, {KindKingBomb, 16, 5}}, []string{"AAAA_5", "XKING5"}},
		{"六大王双解读", rep(6, 17), []Shape{{KindBomb, 17, 6}, {KindKingBomb, 17, 6}}, []string{"AAAA_6", "DKING6"}},
		{"七张普通炸", rep(7, 3), []Shape{{KindBomb, 3, 7}}, []string{"AAAA_7"}},
		{"七张同王无王炸", rep(7, 16), []Shape{{KindBomb, 16, 7}}, []string{"AAAA_7"}},
		{"24张上限", rep(24, 3), []Shape{{KindBomb, 3, 24}}, []string{"AAAA_24"}},
		{"25张超限", rep(25, 3), nil, nil},
		{"面值越界不校验", []int{1, 1}, []Shape{{KindPair, 1, 2}}, []string{"AA"}},
		{"顺子不合法", []int{1, 2, 3, 4, 5}, nil, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			shapes := Classify(vals(c.vals...))
			if !reflect.DeepEqual(shapes, c.want) {
				t.Fatalf("Classify(%v) = %+v, 期望 %+v", c.vals, shapes, c.want)
			}
			for i, s := range shapes {
				if s.TypeName() != c.wantNames[i] {
					t.Fatalf("TypeName[%d] = %s, 期望 %s", i, s.TypeName(), c.wantNames[i])
				}
			}
		})
	}
}

// shapeOf builds a table shape from the legacy lastCardInfo form (type string + key).
func shapeOf(typ string, key, length int) Shape {
	var n int
	switch {
	case typ == "A":
		return Shape{KindSingle, key, 1}
	case typ == "AA":
		return Shape{KindPair, key, 2}
	case typ == "AAA":
		return Shape{KindTriple, key, 3}
	case typ == "AAAA":
		return Shape{KindBomb, key, 4}
	case typ[0] == 'A':
		fmt.Sscanf(typ, "AAAA_%d", &n)
		return Shape{KindBomb, key, n}
	case typ[0] == 'X':
		fmt.Sscanf(typ, "XKING%d", &n)
		return Shape{KindKingBomb, int(Jo), n}
	default:
		fmt.Sscanf(typ, "DKING%d", &n)
		return Shape{KindKingBomb, int(JO), n}
	}
}

func beatsAny(candidate []int, table Shape) bool {
	for _, s := range Classify(vals(candidate...)) {
		if s.Beats(table) {
			return true
		}
	}
	return false
}

// TestBeatsMatrix mirrors game's TestValidateMatrix.
func TestBeatsMatrix(t *testing.T) {
	cases := []struct {
		name      string
		tableTyp  string
		tableKey  int
		tableLen  int
		candidate []int
		ok        bool
	}{
		{"单张压单张", "A", 5, 1, []int{6}, true},
		{"单张不够大", "A", 5, 1, []int{4}, false},
		{"单张同值不可压", "A", 5, 1, []int{5}, false},
		{"对子压对子", "AA", 8, 2, []int{9, 9}, true},
		{"对子压不动小对", "AA", 8, 2, []int{7, 7}, false},
		{"单张不能压对子", "AA", 8, 2, []int{14}, false},
		{"三条不能压对子", "AA", 8, 2, []int{14, 14, 14}, false},
		{"炸弹压单张", "A", 14, 1, []int{4, 4, 4, 4}, true},
		{"五炸压四炸", "AAAA", 14, 4, []int{3, 3, 3, 3, 3}, true},
		{"四炸压不动四炸同值", "AAAA", 14, 4, []int{14, 14, 14, 14}, false},
		{"三王压≤5炸", "AAAA_5", 14, 5, []int{16, 16, 16}, true},
		{"三王压不动6炸", "AAAA_6", 14, 6, []int{16, 16, 16}, false},
		{"六王压≤11炸", "AAAA_11", 14, 11, []int{17, 17, 17, 17, 17, 17}, true},
		{"六王压不动12炸", "AAAA_12", 14, 12, []int{17, 17, 17, 17, 17, 17}, false},
		{"大王三压小王三", "XKING3", 16, 3, []int{17, 17, 17}, true},
		{"小王三压不动大王三", "DKING3", 17, 3, []int{16, 16, 16}, false},
		{"同级小王压不动小王", "XKING3", 16, 3, []int{16, 16, 16}, false},
		{"四王压三王张数多", "DKING3", 17, 3, []int{17, 17, 17, 17}, true},
		{"三王压不动四王", "XKING4", 16, 4, []int{17, 17, 17}, false},
		{"四王压不动六王", "DKING6", 17, 6, []int{16, 16, 16, 16}, false},
		{"四炸压不动三王", "DKING3", 17, 3, []int{4, 4, 4, 4}, false},
		{"五炸压不动三王", "DKING3", 17, 3, []int{4, 4, 4, 4, 4}, false},
		{"六炸反压三王", "DKING3", 17, 3, []int{4, 4, 4, 4, 4, 4}, true},
		{"七炸压不动四王", "XKING4", 16, 4, []int{4, 4, 4, 4, 4, 4, 4}, false},
		{"八炸反压四王", "XKING4", 16, 4, []int{4, 4, 4, 4, 4, 4, 4, 4}, true},
		{"三炸不是炸弹压不了王炸", "DKING3", 17, 3, []int{4, 4, 4}, false},
		{"两对杂牌压不了任何", "DKING3", 17, 3, []int{4, 4, 5, 5}, false},
		{"三王作为三压三2", "AAA", 15, 3, []int{16, 16, 16}, true},
		{"四小王作为四炸压四A", "AAAA", 14, 4, []int{16, 16, 16, 16}, true},
		{"五小王压4炸走王炸解读", "AAAA", 14, 4, []int{16, 16, 16, 16, 16}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := beatsAny(c.candidate, shapeOf(c.tableTyp, c.tableKey, c.tableLen)); got != c.ok {
				t.Fatalf("beatsAny(%v, %s/%d/%d) = %v, 期望 %v", c.candidate, c.tableTyp, c.tableKey, c.tableLen, got, c.ok)
			}
		})
	}
}

// TestBeatsAntisymmetric checks the beat relation is antisymmetric.
func TestBeatsAntisymmetric(t *testing.T) {
	r := rand.New(rand.NewSource(2))
	shapes := []Shape{}
	for i := 0; i < 300; i++ {
		n := 1 + r.Intn(6)
		v := 3 + r.Intn(15)
		vs := make([]int, n)
		for j := range vs {
			vs[j] = v
		}
		shapes = append(shapes, Classify(vals(vs...))...)
	}
	for _, a := range shapes {
		for _, b := range shapes {
			if a.Beats(b) && b.Beats(a) {
				t.Fatalf("压制关系非反对称: %+v / %+v", a, b)
			}
		}
	}
}

// TestCardJSONCodec asserts byte-exact wire compatibility and round-trips.
func TestCardJSONCodec(t *testing.T) {
	b, err := json.Marshal(Card{Face: JO, Suit: Heart})
	if err != nil || string(b) != `{"value":17,"type":0}` {
		t.Fatalf("Marshal = %s err=%v", b, err)
	}
	var c Card
	if err := json.Unmarshal([]byte(`{"value":3,"type":2}`), &c); err != nil || c.Face != Face3 || c.Suit != Spade {
		t.Fatalf("Unmarshal = %+v err=%v", c, err)
	}
}

func TestNewDeck(t *testing.T) {
	deck := NewDeck()
	if len(deck) != 324 {
		t.Fatalf("总张数 %d != 324", len(deck))
	}
	count := map[Face]int{}
	for _, c := range deck {
		count[c.Face]++
	}
	for v := Face3; v <= Face2; v++ {
		if count[v] != 24 {
			t.Fatalf("面值 %v 张数 %d != 24", v, count[v])
		}
	}
	if count[Jo] != 6 || count[JO] != 6 {
		t.Fatalf("王张数异常: jo=%d JO=%d", count[Jo], count[JO])
	}
}

func TestScoreAndOrdinal(t *testing.T) {
	scoreCases := map[Face]int{Face3: 0, Face5: 5, Face10: 10, FaceK: 10, FaceA: 0, Jo: 0, JO: 0}
	for f, want := range scoreCases {
		if got := (Card{Face: f}).Score(); got != want {
			t.Fatalf("Score(%v) = %d != %d", f, got, want)
		}
	}
	if !reflect.DeepEqual([]int{Card{Face: Face3}.Ordinal(), Card{Face: JO}.Ordinal()}, []int{1, 15}) {
		t.Fatal("Ordinal 应为 1-15")
	}
}
