// dict.go immutable rule data tables: face/suit display names, score values,
// legal king-bomb lengths. Rule data changes go here only.
package card

var faceNames = map[Face]string{
	Face3:  "3",
	Face4:  "4",
	Face5:  "5",
	Face6:  "6",
	Face7:  "7",
	Face8:  "8",
	Face9:  "9",
	Face10: "10",
	FaceJ:  "J",
	FaceQ:  "Q",
	FaceK:  "K",
	FaceA:  "A",
	Face2:  "2",
	Jo:     "jo",
	JO:     "JO",
}

// suitNames uses the legacy A/B/C/D letters, not standard suit symbols.
var suitNames = map[Suit]string{
	Heart:   "A",
	Diamond: "B",
	Spade:   "C",
	Club:    "D",
}

var scoreTable = map[Face]int{
	Face5:  5,
	Face10: 10,
	FaceK:  10,
}

// kingBombLens: 7+ identical jokers keep only the normal-bomb interpretation
// (unreachable with 6 decks, kept for legacy semantics).
var kingBombLens = map[int]bool{3: true, 4: true, 5: true, 6: true}
