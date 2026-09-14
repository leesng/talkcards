// static.go 机器人/托管出牌策略的可调常量集中地。
// 机器人和托管共用 internal/bot 策略，调整概率只需改这里；
// 同步检查 bot_test.go 中 TestFollowSmartBombChance 的用例期望值。
package bot

// 小牌阈值：队友出的 ≤8 的单/双/三视为小牌（可以让队友吃走拿牌权）
const (
	smallTopRankMax = 8
)

// 只剩炸弹能大过时的放行概率（按桌面分档）：
// pot == 0（桌面无分）分档
const (
	bombPot0SmallRank = 0.20 // 4 张且牌面 ≤8
	bombPot0BigRank   = 0.10 // 4 张且牌面 >8
	bombPot0Big       = 0.05 // 超 4 张（含王炸）
)

// 0 < pot ≤ 50（桌面有分）分档；pot > 50 时必压（概率恒为 1，无常量）
const (
	bombPot50Len4  = 0.80 // 4 张
	bombPot50Len5  = 0.40 // 5 张
	bombPot50Len6P = 0.10 // 6 张及以上（含王炸）
)
