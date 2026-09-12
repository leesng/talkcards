// dict.go 牌规则字典表（纯数据常量集中存放）：面值显示名、花色显示名、
// 分牌分值、王炸合法张数。均为不可变数据，供包内各处引用；改动规则数据只看这里。
package card

// faceNames 牌面显示名（10 及以下数字，J/Q/K/A，jo/JO）
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

// suitNames 花色显示名（历史编码沿用 A/B/C/D，非标准花色符号）
var suitNames = map[Suit]string{
	Heart:   "A",
	Diamond: "B",
	Spade:   "C",
	Club:    "D",
}

// scoreTable 分牌分值（5 计 5 分，10/K 计 10 分，缺省 0 分）
var scoreTable = map[Face]int{
	Face5:  5,
	Face10: 10,
	FaceK:  10,
}

// kingBombLens 王炸合法张数表（7 张以上同王只剩普通炸解读，不可达但保留旧语义）
var kingBombLens = map[int]bool{3: true, 4: true, 5: true, 6: true}
