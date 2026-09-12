# AGENTS.md

Multiplayer Chinese card game (沟通牌, "running quick" style). Go backend (`backend/`): Echo + 纯 WebSocket 通信层（github.com/coder/websocket），前端静态资源嵌入单二进制。原 Node 版（server.js / game.js / core-validator.js，Express + Socket.IO v2）已删除，仅存于 git 历史。

## Commands

### Go (backend/)

- `(cd backend && go run ./cmd/server)` — dev run, default port **8000** (override with `--port/-p` flag or `PORT` env). CLI flags via spf13/pflag: `--port/-p`, `--host`, `--reconnect-timeout`（秒，0=无限）, `--db`（战绩库路径，默认 `./talkcards.db`）, `--log-level`（debug|info|warn|error，默认 info）, `--log-format`（text|json，默认 text）, `--version/-v`. 战绩持久化恒开，无开关；SIGINT/SIGTERM 走 `e.Shutdown` 优雅退出并排空异步落库队列（不再 `os.Exit` 跳过 defer）。
- 日志系统为 `log/slog`（`internal/logging` 初始化，输出 stderr）：hub/wssrv 经构造函数注入 `*slog.Logger`，业务字段用结构化键（user/desk/pos/event/err）；`slog.SetDefault` 已在 main 中设置。
- `(cd backend && go vet ./... && go test ./...)` — unit tests (wssrv 通信层、card validator、game logic).
- `./build.sh` (repo root) — cross-compiles `target/talkcards-linux-{amd64,arm64}` single binaries (git-ignored). `./build.sh test` builds the native-arch binary, boots it on a temp port (`TEST_PORT`, default 8765), and runs the full-game audit `e2e-audit.js` against it.

### E2E 验收

- `node backend/scripts/e2e-bot.js <url>`（默认 http://127.0.0.1:8000，需先在仓库根 `npm install`——devDependencies 提供 `ws` 包）— 5 大场景：大厅（用户名登录校验）、8 机器人完整牌局、游戏中掉线（保留→超时/主动终止）、断线重连续局、历史战绩查询。多轮连跑很重要（状态泄漏只会在跨轮时显现）；每轮用 `timeout` 限界防止挂死。
  - URL 亦可用 `E2E_URL` 环境变量传（`node -e`/require 复用 WsClient/Bot 时 argv 不属于脚本）。
  - 场景 B 兼容任意 `--reconnect-timeout`：短超时服务器走"超时判逃"，长超时服务器 6s 后由他人主动退出触发终止（manual 路径）。
- `node backend/scripts/e2e-audit.js <url>` — 完整对局审计：8 机器人按「有牌权：有单（孤张）出单，无单出对，无对出三，最后才是炸弹」策略出牌——候选按 孤张单→对→三→炸弹 分组、组内牌值升序，同值张数整体归组不拆（跟牌宁可过牌不拆对/三）；炸弹候选按掷骰放行（桌上有分 90%、无分 10%，未命中跳过），手里只剩炸弹时不掷骰直接出最小炸弹。逐手输出台账（含收分），内置独立重放校验器逐帧断言轮转/压牌合法性/牌数守恒/收分一致。`AUDIT_VERBOSE=1` 输出每次被拒尝试与跳过的炸弹，`AUDIT_FRAMES=1` 输出原始帧 JSON。发牌与掷骰均随机（无 seed，排查靠台账/原始帧）；`./build.sh test` 默认连跑 3 局（`AUDIT_ROUNDS` 覆盖）。

## Architecture

Go 版（`backend/`，2026-09 已完成现代化重构 P0–P3，计划见 `docs/backend-refactor-plan.md`）：
- `internal/wssrv/` — 通信层：`/ws` 端点 WebSocket 升级（coder/websocket），单层 JSON 信封 `{"type":"<事件名>","data":<载荷>}`（data 可缺省）。每连接一个读循环（串行派发事件，对应 Node 单线程语义）+ 一个写循环（串行发送 + 25s 周期 ping、60s pong 超时兜底）。`Conn.Emit` 非阻塞（256 帧缓冲，满则丢弃记日志）。
- `internal/card/` — 牌与牌型：`Card{Face,Suit}`（MarshalJSON 产 `{value,type}`）、`Classify`/`Beats`/`Shape.TypeName`（"A"/"AA"/"AAA"/"AAAA"/"AAAA_n"/"XKINGn"/"DKINGn"）；字典常量（面值名/花色名/分牌分值/王炸张数表）集中在 `dict.go`。
- `internal/game/` — 牌局状态机（对齐 game-rules.md 的有意偏离版）：`Phase`(Idle/Call/Playing/Over/Redeal/Error)、规则链 `playRules`、`Play(pos,cards) PlayResult{Accepted,Applied,HasShape,Shape}`（越序合法牌 → Accepted+!Applied+PhaseError）、`CallScore`；快照 getter `SumFeng()/CalledScores()` 返回 **[8]int 数组**、`Hands()` 返回 `[]HandSnapshot{PosID,Cards}`（wire 数字键对象、红桃统计 `HT`、`Result` 的 JSON 字段名均由 hub translator/events 承载，game 包无 json tag）；`settle.go TeamScores()` 供落库。
- `internal/table/` — 桌位聚合 `Lobby/Desk/Seat/Hold/PlaySnapshot`：20×8 座生命周期（Seat 即 wire 载荷结构）、`Desk.Game` 挂载对局、开局快照（StartedAt/Players）、`LastPlay` 出牌重连快照、`Holds` 断线保留（倒计时器留在 hub——回调须持锁）；`QuickJoin` 空位最少桌优先。不依赖 wssrv。
- `internal/account/` — `Person{UID,Name,PassHash 预留}` + `ValidateName`（空名/超长返回哨兵错误，hub 映射为登录失败文案）；用户名即唯一身份、不落库。
- `internal/hub/` — GameServer 等价物（移植自原 Node 版 server.js），按职责分文件：`hub.go`（Hub/路由/大厅房间 handler，≤400 行）、`gameflow.go`（叫分/出牌/开局/终止/断线保留）、`session.go`（Session 持 `*account.Person` + 在线会话注册表 `sessionRegistry`，list 保序/index O(1)）、`broadcast.go`（持锁快照入队 + pump 串行发送）、`translator.go`（领域↔wire 载荷、数组→数字键对象、重连帧重建、GAME_START 红桃统计）、`events.go`（事件契约）、`persist.go`（异步落库队列 + `Flush` 排空）。所有 handler 在单一互斥锁下串行执行；落库经单 worker 串行写库、历史查询在锁外执行，避免磁盘 I/O 阻塞全局锁。`wire_test.go` 黄金帧测试：fakeConn 直调 handler，结构体解码断言（含整局驱动至 GAME_OVER + `Flush` 后落库校验）。
- `internal/store/` — 战绩持久化（GORM + glebarez/sqlite 纯 Go 驱动，AutoMigrate 建表，不考虑旧版库文件兼容）。`UserName` 需显式 `column:username`（GORM 默认 snake_case 成 user_name）；SaveGame 用显式事务两步插入（关联保存会先插子行且带 upsert 语义）。
- `web/embed.go` — embed `web/static/`。
- `cmd/server/main.go` — Echo 挂载 `/ws` + 静态资源。

前端（`backend/web/static/`，jQuery + Vue 2 + layer.js）：

- 通信层 `js/mySocket.js`：socket.io 风格 on/emit 迷你客户端（断线指数退避重连 + 自动重登录），信封协议与 wssrv 对应。
- `index.html` 内联 Vue 应用，所有 `.on/.emit` 与旧 Socket.IO 事件契约完全一致。
- `js/parser.js` 复制了后端压牌校验（internal/card）的辅助函数供客户端用——改压牌校验时同步检查。

## Gotchas

- 通信协议已从 Socket.IO v2 迁移为纯 WebSocket + JSON 信封（历史决策记录：曾用 vendored go-socket.io fork + 3 补丁，后整体替换）。原 Node 版（socket.io 协议）已删除，仅存于 git 历史。
- e2e 客户端（e2e-bot.js）铁律：tryPlay 不设超时（拥堵时帧晚到被误判失败会造成手牌脱同步）；重连后必 `wait('GAME_START')` 显式等重放帧（两帧分属不同事件循环批次时直接读 pending 会扑空）并以服务器手牌为准 takeCards 对齐；spawnBots 每次运行随机选桌（死局/保留座位残留 600s 会占死固定桌号）。
- Go 版状态仍以内存为主，对局战绩经 SQLite 持久化（`./talkcards.db`，恒开无开关）。登录以用户名为唯一身份（2026-09 产品决策，注册/密码已删除）：非空、≤10 字、不与在线重名即可，不落库；对局记录（normal/escape 摘要）与断线重连保留无关，重启后可查历史（按用户名归属），进行中对局重启即失。
- 断线重连：游戏进行中（叫分/出牌）掉线保留座位（座位/手牌/轮转原样），`--reconnect-timeout`（默认 600s，0=无限）内重登自动坐回（LOGIN_SUCCESS 后紧跟 RECONNECT + 按阶段重放 GAME_START[当前手牌]/CTX_USER_CHANGE 或 SHOW_TOP_CARD+CTX_PLAY_CHANGE[缓存帧，首出前掉线则补发轮转帧——否则重连者不知轮到自己会死锁对局]）；超时未归判逃跑终止并落库。非游戏中掉线/主动退出仍即时释放座位。
- 事件契约见 events.go：LOGIN 载荷为纯用户名字符串（json string），RECONNECT{deskId,posId,posInfos}、HISTORY_LIST{page}→HISTORY_LIST{list,total,page}、HISTORY_DETAIL{gameId}→HISTORY_DETAIL（仅本人参与局可查）、HISTORY_FAIL。前端 index.html 登录页仅用户名输入框，大厅有"我的战绩"入口。
- Card encoding (`originalCards`): `value` 3–13 normal, 14=A, 15=2, 16=small joker, 17=big joker; `type` 0=hearts, 1=diamonds, 2=spades, 3=clubs; deck is 6×54=324 cards.
- Bug-for-bug compat is intentional in the Go port: GAME_START broadcasts all 8 hands, unused `score` in CALL_SCORE, escape flow setting empty seats' state — don't "fix" these without comparing against 原 Node 版 server.js（已删，查 git 历史）。原 `updateRoomStatus(desk,posID)` 传参怪癖已于 P3 修正为传 0（已查证前端只消费 deskId/positions[].posId/state/userName，不读 desk.state）。另一处有意收紧：原 Validate 无阶段门（叫分阶段出 4+ 张炸弹可"压空桌"并把对局转入 Playing、空 PLAY_CARD 等效叫分），Go 版规则链含 rulePhasePlaying，非出牌阶段的 PLAY_CARD 一律拒绝且无状态变更。
- 规则已**有意偏离** Node 原版（2026-09 产品决策，见 game-rules.md），比对 git 历史时注意三处：① whoFirst 改为红桃3最多→比3总数→随机（原为红桃字典序含王）；② 王炸大小改为张数优先、同张数大王>小王，普通炸弹反压王炸需张数>2N-1（原 2N-1 阈值下大小王等价且允许 3王压4王、4炸压3王）；③ 全队出完终局带走败方未出手分牌，Result.Score 语义从"叫分"改为"胜队最终得分"（前端 GAME_OVER 按 score*ratio*2 分摊显示）。e2e-audit.js 的 Replayer.beats 已同步压牌规则，改 game.Validate 必须同步改它。
- `game.Play` 越序（posID != 轮转者）会把状态机置为 PhaseError 且不再广播有效回合——客户端必须严格按 CTX_PLAY_CHANGE 的 posId 出牌（e2e 的 myTurn/busy 守卫即为此）。
- 纯 WS 下背靠背多帧可能在同一 tick 派发：e2e 客户端用 pending 缓冲 + wait() 显式消费解决"监听器晚于帧注册"的丢事件问题；**动态 on/off 绝不能消费 pending**（误消费旧响应会造成幽灵完成，曾引发连锁越序出牌）。
- `ws.close()` 的关闭帧为异步发送，测试进程 exit 前等 ~150ms 让服务器收到断开并清理座位。
- Process management: `pkill/pgrep -f` patterns can match your own shell command line and self-kill; prefer `fuser -k <port>/tcp` or `pgrep -x <exact-binary-name>`.
- Comments, commit messages, and UI strings are in Chinese.
