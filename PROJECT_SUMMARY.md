# 沟通牌（talkcards）工程逻辑与特性汇总

> 目的：本文档汇总本工程的架构、各模块职责、核心流程、事件协议、游戏规则与已知陷阱，
> 供后续直接基于本文继续分析与增强功能，**无需再通读全部源码**。
> 若涉及具体代码，文中给出 `文件路径:行号` 便于定位比对。规则变更/重构前请同步阅读
> 根目录的 `AGENTS.md`、`game-rules.md` 与 `docs/backend-refactor-plan.md`。

---

## 1. 项目概览

- 一款 8 人（4v4）扑克「跑得快」类团队变种（源自华为内部玩法）的线上化实现。
- 后端为 Go **单二进制**（前端静态资源已 `embed` 嵌入），持久化用 SQLite，通信为**纯 WebSocket**。
- 招牌玩法「沟通」：交流**完全公开**——队友可明着商量战术，但发言全场广播、对手也听得到，可放烟雾弹。
- 前端：jQuery + Vue 2 + layer.js（无构建步骤，直接内联在 `index.html` 里）。

---

## 2. 技术栈

| 层 | 技术 |
|---|---|
| 后端 | Go、Echo（HTTP/静态）、`github.com/coder/websocket`（纯 WS）、`spf13/pflag`（CLI） |
| 持久化 | GORM + `glebarez/sqlite`（纯 Go 驱动），`./talkcards.db`，AutoMigrate 建表 |
| 日志 | `log/slog`（`internal/logging`），输出 stderr，结构化字段 |
| 前端 | Vue 2（`vue.min.js`）、`mySocket.js`（自研 socket 迷你客户端）、`parser.js`（复刻后端压牌校验）、layer.js |

---

## 3. 目录结构

```
talkcards/
├── build.sh                     # 交叉编译单二进制 / test(构建+对局审计) / run(磁盘直跑)
├── game-rules.md                # 完整游戏规则
├── AGENTS.md                    # 维护者速查（架构 + 铁律 + Gotchas）
├── PROJECT_SUMMARY.md           # 本文档
├── backend/
│   ├── cmd/server/main.go       # 入口：Echo 挂 /ws + 静态资源，优雅关闭
│   ├── internal/
│   │   ├── wssrv/               # WebSocket 通信层（transport）
│   │   ├── hub/                 # 业务总控（GameServer 等价物）
│   │   ├── table/               # 大厅/桌/座位聚合
│   │   ├── game/                # 牌局状态机（纯领域，无 I/O）
│   │   ├── card/                # 牌/牌型/压牌比较（纯函数）
│   │   ├── bot/                 # 服务器机器人 + 托管策略
│   │   ├── account/             # 用户身份（用户名即身份，不落库）
│   │   ├── store/               # SQLite 战绩持久化
│   │   └── logging/             # slog 初始化
│   ├── web/embed.go             # embed web/static
│   ├── web/static/              # 前端（index.html + css + js）
│   └── scripts/                 # e2e-bot.js / e2e-audit.js / spectate-smoke.js
└── docs/backend-refactor-plan.md # 现代化重构计划（P0-P3）
```

---

## 4. 构建与运行

```sh
cd backend && go run ./cmd/server        # 开发模式（前端走磁盘 web/static）
./build.sh                               # 交叉编译 linux/amd64、linux/arm64、windows/amd64
./build.sh run                           # 不构建，go run + --static-dir 磁盘直跑（改前端即刷）
./build.sh test                          # 构建本机 + 完整对局审计（默认 3 局）
cd backend && go test ./...              # 单元测试
node backend/scripts/e2e-bot.js          # e2e 协议验收
```

CLI 参数：`--port/-p`(8000)、`--host`、`--reconnect-timeout`(秒,600,0=无限)、`--db`、`--log-level`、`--log-format`、`--static-dir`、`--bot-delay-min-ms/-max-ms`、`--version/-v`。
战绩持久化恒开、无开关；SIGINT/SIGTERM 走 `e.Shutdown` 优雅退出并 `Flush` 排空落库队列。

---

## 5. 通信协议

- 端点：`/ws`；单层 JSON 信封：`{"type":"<事件名>","data":<载荷>}`（data 可缺省）。
- 每连接一个读循环（串行派发 = 对齐旧 Node 单线程语义）+ 一个写循环（串行发送、25s ping、60s pong 兜底）。
- `Conn.Emit` 非阻塞，256 帧缓冲、满则丢弃记日志。
- 客户端 `mySocket.js`：指数退避重连、**断线自动重发 LOGIN**（`lastLogin`）。

### 入站事件（前端→后端）
`LOGIN` / `QUICK_JOIN` / `SITDOWN` / `UNSITDOWN` / `PREPARE` / `CANCEL_PREPARE` /
`HOST_START_GAME`(可带 `{fillBots:true}`) / `PLAY_CARD` / `USER_MESSAGE` / `HISTORY_LIST` / `HISTORY_DETAIL` / `TOGGLE_TRUSTEE` / `SPECTATE`

### 出站事件（后端→前端）
`LOGIN_SUCCESS/FAIL`、`RECONNECT`、`QUICK_JOIN`、`SITDOWN_SUCCESS/ERROR`、`UNSITDOWN_SUCCESS`、
`REFRESH_LIST`、`STATUS_CHANGE`、`POS_STATUS_CHANGE`、`POS_STATUS_RESET`、`ROOM_STATUS_CHANGE`、
`FORCE_EXIT_EV`、`PREPARE_SUCCESS`、`CANCEL_PREPARE_SUCCESS`、`GAME_START`、`SHOW_TOP_CARD`、
`CTX_PLAY_CHANGE`、`PLAY_CARD_SUCCESS/ERROR`、`GAME_OVER`、`MESSAGE`、`USER_MESSAGE`、
`HOST_CHANGE`、`TRUSTEE_CHANGE`、`SPECTATE_SUCCESS/ERROR`、`HISTORY_LIST/DETAIL/FAIL`

> 事件常量与载荷结构定义在 `backend/internal/hub/events.go`（**与 `index.html` 逐字段对应，改动必须同步**）。

---

## 6. 后端架构（分层职责）

### 6.1 `internal/wssrv` —— 通信层
`server.go`：WebSocket 升级、信封解析、读写泵、`OnEvent/OnDisconnect` 注册、`Conn` 接口。
纯传输，不理解业务。

### 6.2 `internal/card` —— 牌与牌型（纯函数，前端 `parser.js` 须同步）
- `card.go`：`Card{Face,Suit}`；wire 编码 `{value,type}`（value 3-13、14=A、15=2、16/17=大小王；type 0♥1♦2♠3♣）；`NewDeck` = 6×54 = 324。
- `dict.go`：面值/花色/分牌（5=5 分、10/K=10 分）/王炸合法张数（3~6）常量表。
- `shape.go`：只有 5 种牌型 `Kind`（单/对/三/炸弹/王炸）；`Classify` 给出一手牌的所有合法释义（如 4 张小王 = 炸弹 + 王炸）；`TypeName` 产 `"A"/"AA"/"AAA"/"AAAA"/"AAAA_n"/"XKINGn"/"DKINGn"`；`Beats` 实现压牌矩阵（见 §9）。

### 6.3 `internal/game` —— 牌局状态机（纯领域，无 I/O、无 JSON tag）
`game.go`：`Phase`(Idle/Playing/Over/Error)、`seats[8]`、`trick`(桌心当前压手+滚动分)、`turn`/`leader`。
- `Start()` 直接进入出牌阶段（**无叫分阶段**）；`deal()` 发牌（每家 40 张 + 余 4 张按 `last4` 随机补 1）；`whoFirst()` 红桃3最多→比3总数→随机（**有意偏离 Node 原版**）。
- `Play(pos,cards)` → `PlayResult{Accepted,Applied,HasShape,Shape,Rejected}`：**先查轮转**——越序（`pos != turn`）直接 `Rejected{Rule:"turn"}`、无任何状态变更；空牌=过牌直接 apply；其余走规则链 `playRules` = `rulePhasePlaying→ruleHandHas→ruleClassify→ruleBeatTable` 校验后 apply。**（2026-09 上游 `2753cdf` 已移除旧版的「越序毒化」语义）**
- `apply` 负责轮转推进 / 收分 / 接风（`nextGroupPosID`，出完后队友接牌权）/ `isGameOver`（全队出完带走败方未出手分 / 出完者抓分累达 300）；`settle.go` `TeamScores()` 供落库。
- 异常恢复：`Game` 提供 `Snapshot()/Restore()`，`hub` 在桌的 `Desk.LastGood` 保存最近一次有效状态；意外进入 `PhaseError` 时由 `recoverGame` 回滚并 `replayGameFrames` 全桌重放。

### 6.4 `internal/table` —— 大厅/桌/座位
`table.go`：`Lobby`(20 桌 × 8 座)、`Desk`(挂 `Game`、开局快照 `StartedAt/Players`、`LastPlay/LastValidPlay`、`Holds` 断线保留)、`Seat`(state 0空/1未准备/2已准备 + `IsBot` + `Trustee`)。
- `QuickJoin`：空位最少桌优先；`AssignHost` 首坐者当主持（断线保留主持权，真正离席才转移 `TransferHostFrom`，机器人不接班）。
- `FillBots`：空位填机器人（名字 `机+桌号62进制2位+座位号`，桌内唯一）；`ClearBots` 终局清座。
- 不依赖 wssrv。

### 6.5 `internal/hub` —— 业务总控（GameServer 等价物）
按职责分文件，**所有 handler 在单一 `h.mu` 下串行执行**，发送走异步 pump，落库走异步单 worker（避免磁盘 I/O 阻塞全局锁）：
- `hub.go`：路由注册、登录/进出房间/准备/开牌/托管切换/观战、`seatedClient`(真人座位)/`clientInRoom`(含观战) 守卫。
- `gameflow.go`：出牌 `onPlayCard`、`applyPlayResult`（真人/机器人共用；**越序被拒→重推轮转帧 resync**、`Clear` 标记、`PhaseError` 时 `recoverGame` 回滚、终局落库清座）、机器人定时出动 `scheduleBotIfTurn/botAct`、断线保留/超时 `onReconnectTimeout`、开局/终止 `startGame/terminateGame/recordGame`。
- `session.go`：`Session{conn,person,deskID,posID,out}`；`sessionRegistry`（list 保序 + map O(1)）。
- `broadcast.go`：`emit`（单发入队）、`broadCastHouse`（大厅）、`broadCastRoom`（桌内，可排除发起者）、`broadCastGameStart`（玩家全量手牌 / 观战者脱敏）。
- `translator.go`：领域→wire 载荷（数组→`{"0":..}` 数字键对象、重连帧重建、红桃统计）。
- `events.go`：事件契约与载荷结构。
- `persist.go`：异步落库队列 + `Flush` 排空。

### 6.6 `internal/bot` —— 机器人 / 托管策略
- `bot.go`：`Candidates`（按面值分组 单→对→三→炸弹，不拆组，炸弹先张数后牌面升序）、`Lead`（≤8 小牌优先按牌面升序，小牌清完按组序）、`Follow`、`FollowSmart`（队友大牌不吃只吃对手；无整组可压对手单时可拆 A/2/王恰 2 张对子拆一张跟；只剩炸弹时按 `static.go` 概率档放行 / 桌上有分>50 必压）、`splitHighPair`。
- 随机源可注入便于测试；概率常量集中在 `bot/static.go`，改动需同步 `bot_test.go`。
- 托管（Trustee）与机器人共用 `botTurn/botAct` 代打链路，规则完全一致；托管中手动 `PLAY_CARD` 被拒。

### 6.7 `internal/account` / `internal/store` / `internal/logging`
- `account.go`：`ValidateName`（空名(`不能为空`)/超10字(`名字不超过10个字`) 哨兵错误）；`Person{UID,Name,PassHash预留}`，用户名即唯一身份、不落库、在线重名即拒。
- `store.go`：`GameRecord` 落库（显式事务两步插入），`HistoryList`(分页)/`GameDetail`(仅本人参与可查)；`games` + `game_players` 两表，时间存 RFC3339 文本。
- `logging`：slog 初始化，text/json 格式。

---

## 7. 前端架构（`backend/web/static/`）

- `index.html`：内联 Vue 2 应用，`where` 状态机 0=登录页 / 1=大厅 / 2=房间（含观战）。
- `js/mySocket.js`：socket.io 风格 `on/emit` 迷你客户端（断线指数退避 + 自动重登录）。
- `js/parser.js`：**复刻**后端 `internal/card` 的 `Classify`/`Beats`/提示逻辑，供客户端校验与"提示"选牌。改压牌规则须两边同步。

### 关键前端数据结构
- `roomState`：`{state(0准备/2出牌/3结束), ctxPos(相对自己 p0-p7), ctxCard(上家牌型), tmpFeng, timeout}`。
- `posState.p0~p7`：每方向座位 `{state, cards, ctxCards, isPass, isDizhu, isBot, trustee, name, sumFeng}`，其中 **p3 = 自己**。
- `desks`：大厅桌列表；`hostPosId`：主持座位。
- `getDirectionByPosId(posId)`：把绝对座位映射为相对方向 p0-p7（大表在 `index.html` 的 `mapping`，按 `this.posId` 选行）。
- 计时器：`startTimer(fn)` 倒计时，到点触发 `autoPlayCards`（托管/观战/非轮到时跳过）。

### 关键事件处理（前端 `mounted` 里注册）
- `GAME_START`：`initCards` 重置 8 家手牌与座位状态。
- `SHOW_TOP_CARD`：进入出牌态、指定先手 `dizhuPosId`、合并顶牌。
- `CTX_PLAY_CHANGE`：更新桌心出牌、各座位抓分、游标（清除标记 `clear`/重放 `replay`/过牌 `isPass`）。
- `GAME_OVER`：弹结算窗（两队得分/剩牌/MVP/炸弹统计/用时轮数）。
- `USER_MESSAGE`：聊天消息入 `msgList`（见 §10）。

---

## 8. 核心业务流程

### 8.1 登录 / 大厅
`LOGIN`(纯用户名字符串) → 校验非空/≤10字/不重名 → `LOGIN_SUCCESS`(带回 `lobby.Desks`) → 若该用户名有断线保留座则 `resumeClient` 自动重连。
大厅 `STATUS_CHANGE`(deskState 2=对局中) 维护桌状态；点进行中座位→观战，空位→入座。

### 8.2 入座 / 准备 / 开局
`SITDOWN{deskId,posId}` → 空位校验 → `UpdatePos(state=1)`、首坐者 `AssignHost`、广播 `POS_STATUS_CHANGE`/`STATUS_CHANGE`。
`PREPARE` → state=2；主持人（且 `fillBots` 填充空位机器人）`HOST_START_GAME` → `startGame`：
广播 `GAME_START`(全 8 家手牌, bug-for-bug)、`SHOW_TOP_CARD`(先手)、`CtxPlayChange`(leadFrame)，再 `scheduleBotIfTurn`。

### 8.3 出牌 / 结算
`PLAY_CARD`(牌数组，空=过) → `game.Play` → `applyPlayResult` → 广播 `CTX_PLAY_CHANGE`（+ 需要时 `Clear`）→ 成功者回 `PLAY_CARD_SUCCESS`；非法回 `PLAY_CARD_ERROR`('你的牌不符合规则')。
终局：`recordGame("normal")` 落库 + `GAME_OVER` + 所有座位转未准备 + 清托管/机器人 + `endDeskPlaying`。

### 8.4 断线重连 / 托管
- 游戏中掉线：`holdSeatForReconnect` 保留座位 + 广播"等待重连"，起 `--reconnect-timeout` 计时器。
- 超时：仍有真人→**自动托管**续局；无任何真人→直接按逃跑终止落库。
- 重连：`LOGIN` 命中 hold → `resumeClient` → `RECONNECT` + `replayGameFrames`(GAME_START/SHOW_TOP_CARD/缓存帧)。

### 8.5 观战 / 人机 / 托管
- 观战：`SPECTATE{deskId}` → `SPECTATE_SUCCESS`；GAME_START 脱敏（只给张数）、重放共用 `replayGameFrames`，观战者不可出牌/聊天/准备。
- 人机：`HOST_START_GAME{fillBots:true}` 填空位机器人，服务器单定时器代打。
- 托管：`TOGGLE_TRUSTEE`（对局中、机器人座不可托管）↔ `TRUSTEE_CHANGE` 广播 + 座位"托管"角标。

### 8.6 战绩
`HISTORY_LIST{page}` / `HISTORY_DETAIL{gameId}` 在锁外查库，仅本人参与局可看。

---

## 9. 游戏规则要点（`game-rules.md` + 有意偏离）

1. **牌型**只有：单张 / 对子 / 三条 / 炸弹(4-6 张同面) / 王炸(3-6 张同王)。
2. **压牌矩阵**（`card/shape.go:Beats`）：
   - 王炸 vs 王炸：张数多者胜，同张数大王>小王。
   - 王炸压普通：对方牌张数 ≤ 2N-1。
   - 普通炸弹压王炸：需张数 > 2N-1（且非王）。
   - 炸弹压非炸弹恒胜；炸弹 vs 炸弹：先比张数（非王）再比牌面；同类同张比牌面。
3. **先手**：红桃3最多→3总数→随机（偏离原版字典序）。
4. **终局**：全队出完即胜并带走败方未出手分牌；或出完者抓分累计 ≥300。`Result.Score` = 胜队最终得分。
5. **收分**：5=5 分，10/K=10 分；一墩没人压过由最后出牌者收桌心待收分（`TmpFeng`/pot）。

---

## 10. 公共聊天功能（本次新增，2026-09）

### 背景
后端本已支持桌内聊天（`USER_MESSAGE` → `hub.onUserMessage` → 桌内广播），但前端在"现代化视觉重构"时把聊天 UI 从模板里删掉了（只留下数据 `msgList/msgBox` 与空方法 `sendMessage`，以及闲置 CSS `.msg-box` 等）。本次把「右侧公共聊天面板」补回并接通。

### 后端（无需改动，复用既有）
- `hub/events.go`：`EvUserMessage="USER_MESSAGE"` / `EvUserMessageOut="USER_MESSAGE"`。
- `hub/hub.go:479 onUserMessage`：`seatedClient` 守卫（观战者不能发）→ `broadCastRoom(USER_MESSAGE, deskID, userMessage{Type:"USER",PosID,Msg,ID,Time})`，**桌内全部 8 人可见**（对手也可见，符合"沟通公开"玩法）。
- `userMessage` 载荷含 `{type,posId,msg,id,time}`；`type` 为 `"USER"`(玩家) / `"SYS"`(系统)。

### 前端改动（`backend/web/static/`）
1. **布局**（`index.html`）：房间 `where===2` 区域包一层 `.room-table`（左侧原牌桌），右侧新增 `<aside class="chat-panel">`。
2. **CSS**（`style.css`）：
   - `.room` 改 `display:flex` 左右两栏。
   - `.room-table{flex:1;position:relative;transform:translateZ(0)}` —— **关键**：`transform` 使内部所有 `position:fixed` 元素（牌桌沿/座位/牌列/出牌区/记分区）改以 `.room-table` 为参照，桌面随左栏变窄等比例收缩、几何不回大改。
   - 新增 `.chat-panel` 系列（320px 右侧栏，header/消息列表/快捷短语/输入框）与消息着色（偶数座=蓝 A 队、奇数座=红 B 队；SYS 置中灰字）。
3. **数据/方法**（`index.html`）：
   - `data` 新增 `chatDraft`；复用 `msgList`(消息) / `msgBox`(快捷短语)。
   - `USER_MESSAGE` 处理器补充记录 `posId`（配色用）并 `$nextTick` 滚动到底部。
   - 新方法 `sendMsg()`(读 `chatDraft` 发送) 与 `quickMsg(q)`(发快捷短语)；替换掉旧的 `sendMessage`。
   - 进房/重连/观战时清空 `msgList`（服务端不重放聊天历史）；回大厅 `resetRoomStatus` 也清空。
   - 观战模式：只读（显示"仅供查看"，隐藏输入/快捷）。

### 前端消息渲染
- 玩家消息：`[时间] 名字：内容`，名字按队伍着色。
- 系统消息（进房/退房/掉线/重连/托管/观战/机器人填充等 SYS 广播）置中灰字。

---

## 11. 关键陷阱 / Gotchas（务必遵守）

- **事件契约铁律**：`events.go` 载荷字段名与 `index.html` 逐字对应，改后端结构体字段/JSON tag 必须同步前端 `.on` 处理器。
- **压牌规则三处同步**：改 `card.Shape.Beats`/`Classify` 必须同步 `parser.js`（提示逻辑）与 `e2e-audit.js` 的 `Replayer.beats`。
- **越序出牌**：`game.Play` 越序（posID≠轮转者）现在**拒绝并重推轮转帧 resync**（`PLAY_CARD_ERROR` + `emitTurnResync`），**不再毒化**；意外进入 `PhaseError` 时 `recoverGame` 回滚 `Desk.LastGood`。前端有 `onceAct`（双击冷却）与 `lockAct/unlockAct`（出牌在途锁）防重复出牌；e2e 出牌后仍须**轮询等待** SUCCESS/ERROR 帧再去下一步。
- **重连必等 replay**：e2e 重连后必须 `wait('GAME_START')` 显式等重放帧；两帧分属不同 tick，直接读 pending 会扑空。
- **观战视角**：观战者以 posId=3 视角渲染，`isSpectator` 守卫所有操作/出牌/聊天。
- **背靠背多帧**：纯 WS 下多帧可能同 tick 派发；动态 on/off 绝不能消费 pending（会幽灵完成）。
- **`ws.close()` 异步**：测试进程退出前等 ~150ms 让服务器收到断开并清座。
- **进程管理**：勿用会自杀的 `pkill -f` 模式，用 `fuser -k <port>/tcp` 或 `pgrep -x`。
- **spawnBots**：每次运行随机选桌，遗留保留座位 600s 会占死固定桌号。
- 注释/提交信息/UI 文案均为中文。

---

## 12. 测试与验收

- 单元测试：`wssrv` 通信层、`card` 校验、`game` 逻辑、`table`、`store`、`account`、`bot`、`hub`(wire 黄金帧/机器人整局/单真人 e2e/重连重放/观战/托管)。
- E2E（`backend/scripts/`，需仓库根 `npm install` 装 `ws`）：
  - `e2e-bot.js`：5 大场景（登录校验、8 机器人整局、掉线保留/超时/主动终止、重连续局、历史战绩），兼容任意 `--reconnect-timeout`。
  - `e2e-audit.js`：8 机器人按「有单出单→对→三→炸」策略整局，内置重放校验器逐帧断言轮转/压牌/牌数守恒/收分；`AUDIT_VERBOSE=1`/`AUDIT_FRAMES=1` 调试。
  - `spectate-smoke.js`：真 WS 观战冒烟（attachDriver 须在 HOST_START_GAME 前挂）。
- `./build.sh test` 默认连跑 3 局审计（`AUDIT_ROUNDS` 覆盖）。

---

## 13. 后续增强方向（建议）

1. **聊天持久化 / 历史**：当前聊天仅内存、不落库；可仿照 `store` 增加 message 表与重连回放（参考 `replayGameFrames` 的思路，进房补发最近 N 条）。
2. **私聊 / 队内密语**：当前交流全公开；可加队内频道（按席位奇偶过滤广播，复用 `broadCastRoom` 思路，仅发同队 Session）。
3. **消息限长/频率与敏感过滤**：`onUserMessage` 目前无长度/频率限制，可加 `strings` 长度校验与简单节流。
4. **注册/密码**：`account.Person.PassHash` 已预留字段，可补注册登录与玩家跨端身份。
5. **观战可见聊天**：观战者目前只读聊天但可见；如需要"仅玩家可见策略讨论"可加类型分流。
6. **机器人发言**：可让 bot 在出牌时附带预设 SYS/USER 聊天，增强人机局氛围。