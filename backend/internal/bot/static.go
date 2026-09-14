// static.go tunable bot/trustee strategy constants; changing probabilities
// requires updating TestFollowSmartBombChance expectations in bot_test.go.
package bot

// Small-card threshold: a teammate's ≤8 single/pair/triple counts as small
// (safe for a teammate to take).
const (
	smallTopRankMax = 8
)

// Bomb play probabilities when only a bomb can beat the table, by pot.
// pot == 0 (empty pot) tiers:
const (
	bombPot0SmallRank = 0.20 // 4 cards, face ≤8
	bombPot0BigRank   = 0.10 // 4 cards, face >8
	bombPot0Big       = 0.05 // >4 cards (incl. king bombs)
)

// 0 < pot ≤ 50 tiers; pot > 50 always plays (probability 1, no constant).
const (
	bombPot50Len4  = 0.80 // 4 cards
	bombPot50Len5  = 0.40 // 5 cards
	bombPot50Len6P = 0.10 // 6+ cards (incl. king bombs)
)
