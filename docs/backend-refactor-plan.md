# 后端重构计划（现代化/模块化/面向对象）

> 2026-09 制定并执行。决策：密码预留不启用；允许小改前端（默认零改动）；
> 内部牌编码换新 Face/Suit 模型；执行 P0-P3。

## 0. 决策与硬约束

| 项 | 决定 |
|---|---|
| 账户 | `account.Person{UID, Name, PassHash(预留空串=无密码), Status}`，登录流程不变 |
| 前端 | 默认零改动；仅当确认某 bug-for-bug 在前端无消费方时才顺手清理 |
| 并发模型 | 保留 hub 单互斥锁串行（对齐 Node 单线程语义），领域对象自身不加锁 |
| wire 契约 | 事件名/payload 字段/originalCards 编码逐字冻结；压牌行为不变，故 `js/parser.js` 与 `e2e-audit.js Replayer.beats` 无需改动 |
| 验收 | 每阶段：`go vet ./... && go test ./...` + `./build.sh test`；P2 后加跑短/长两档 `--reconnect-timeout` 的 e2e |

## 1. 目标架构与依赖方向

```
cmd/server ──▶ wssrv（不动）
    │
    ▼
hub/        瘦传输层：事件路由 + 领域事件→wire 翻译 + 广播 + 唯一锁点
 │
 ├──▶ table/    聚合根 Desk：20桌×8座生命周期、准备/开局、断线保留
 │      │
 │      ▼
 │    game/     聚合根 Game：阶段状态机、轮转、Trick、接风、Settler、规则链；纯领域零 I/O
 │      │
 │      ▼
 │    card/     Card 模型 + Classify/Beats + wire 编解码；纯函数
 │
 ├──▶ account/  Person/UID（PassHash 预留）
 └──▶ store/    不动
```

## 2. 核心类型目标形态

### card 包

```go
type Face int16 // 3..K,A,2 → Rank 1..13；Jo=14, JO=15
type Suit int16 // A♥ B♦ C♠ D♣；王无花色
type Card struct{ Face Face; Suit Suit } // 自定义 JSON 编解码 ↔ {"value":3-17,"type":0-3}
// Rank()/Score()/IsJoker()/String()；NewDeck() ×6副=324张

type Kind uint8 // Single Pair Triple Bomb(4-6) KingBomb(3-6纯王)
type Shape struct{ Kind Kind; Rank int; Len int }
func Classify(cards []Card) []Shape      // 全部合法解读（对齐旧 validator 多解读语义）
func (c Shape) Beats(table Shape) bool   // game-rules.md 全矩阵
func (c Shape) TypeName() string         // 复现 "XKING3"/"DKING4"... wire 帧逐字兼容
```

### game 包

```go
type Phase int // Idle/Calling/Playing/Over/Redeal/Error（值沿用 0-5）
type Seat struct { PosID int; Hand []card.Card; Called int; Captured int }
func (s *Seat) Team() int; func (s *Seat) Out() bool // 派生
type Trick struct { Pos int; Top card.Shape; Pot int } // 桌面状态（滚动分）

type PlayContext struct{ Game *Game; Pos int; Cards []card.Card; Shapes []card.Shape }
type Rule func(*PlayContext) *Violation
var pipeline = []Rule{rulePhasePlaying, ruleTurn, ruleHandHas, ruleClassify, ruleBeatsOrPass}

func (g *Game) Deal(); func (g *Game) CallScore(pos int) ([]Event, error)
func (g *Game) Play(pos int, cards []card.Card) ([]Event, error)
// settle.go：Settle(g, reason) → Settlement（含带走分），吸收原 hub.recordGame 逻辑
```

### table 包

`Desk{DeskID, Seats[8]（Empty/Seated/Ready=wire 0/1/2）, Phase, Game, StartedAt, PlayersSnapshot, LastFrame, OfflineHold}`；计时器 AfterFunc 留 hub（须拿锁）。

### hub 包

Session（持 `*account.Person`）+ 路由表 + translator + pump 广播。game/table 不 import wssrv。

## 3. 阶段任务

- **P0 基线**：本文件落盘；`AUDIT_FRAMES=1` 基线帧存档；补 game 特征测试（接风立即触发/Pot 不清算/300 分线/带走分）。
- **P1 card**：新模型 + 编解码 + Classify/Beats/TypeName；新旧对拍（validator_test 全矩阵 + 随机 property）。旧 Validate 暂留门面。
- **P2 game**：game_test 平移先行（红）→ Seat/Trick/pipeline/Play/CallScore/Settle（绿）；发牌算法逐行保留；hub 切新 API；translator 黄金帧测试；删旧 Validate。
- **P3 table+hub**：table 聚合吸收 DeskState/pendingReconnect；account 最小落地；hub 重排 router/translator/broadcast/session；bug-for-bug 清单查证处置；AGENTS.md 更新。

## 4. bug-for-bug 清单（默认 KEEP）

| 行为 | 处置 |
|---|---|
| GAME_START 广播 8 家手牌（前端亮牌/余张依赖） | KEEP |
| 过牌帧 key/type 省略 | KEEP（wire） |
| Next 越序 → StatusError 不再广播 | KEEP（e2e myTurn 守卫依赖） |
| CALL_SCORE 的 score 参数未被采用 | KEEP |
| escape 流把空座位也置 state=1 | KEEP |
| `updateRoomStatus(desk,posID)` 传参怪癖 | P3 查证前端消费方，无消费则修 |
| GetTopCards 恒空、CardGroup.HT 红桃统计 | KEEP（wire） |
| sumCount 只写不读 | 删（内部死状态） |
| posMap 字符串键 | 内部改数组，wire 输出端重建 `{"0":..}` 形态 |

## 5. 风险与缓解

| 风险 | 缓解 |
|---|---|
| P2 轮转/接风/收分边界回归 | 测试先行 + 基线帧字节比对 + audit 多轮连跑 |
| wire 字节漂移 | translator 黄金帧测试；AUDIT_FRAMES=1 抽查 |
| 重连重放回归 | e2e 场景 C/D + 短/长两档 reconnect-timeout |
| 断线保留计时器迁移 | AfterFunc 留 hub，table 只存状态与截止时间 |
