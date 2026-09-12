// settle.go 终局结算：两队最终得分（吸收原 hub.recordGame 的计分逻辑）。
package game

// TeamScores 终局两队最终得分。
// 基线为全队已收分合计；正常终局且全队出完时，胜队改用 Result.Score
// （含带走对方未出手分牌）。escape 终局（Winner 为空）退化为纯收分合计。
func (g *Game) TeamScores() (team0, team1 int) {
	for i := range g.seats {
		if g.seats[i].Team() == 0 {
			team0 += g.seats[i].Captured
		} else {
			team1 += g.seats[i].Captured
		}
	}
	res := g.Result()
	if len(res.Winner) == 4 {
		if res.Winner[0]%2 == 0 {
			team0 = res.Score
		} else {
			team1 = res.Score
		}
	}
	return team0, team1
}
