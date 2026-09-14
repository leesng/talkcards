package game

// TeamScores returns both teams' final scores. Baseline is each team's total
// captured points; on a normal full-team-out finish the winner's score is
// replaced by Result.Score (which includes the losers' unplayed point cards).
// An escape finish (empty Winner) degrades to plain captured totals.
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
