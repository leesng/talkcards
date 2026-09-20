# AI 沟通牌机器人（LLM 驱动的真人玩家）

本目录提供一个以 LLM 驱动决策的「真人玩家」机器人，作为**普通 WebSocket 客户端**接入现有 Go 服务器，与真人玩家完全同构地参与对局与公开聊天。

## 1. 目标与硬约束（源自 `ai-bot-req.txt`）

- 在 Go 服务器之上，提供一个 LLM 驱动决策的 AI 真人玩家；登录、坐下、出牌、过牌、聊天收发等操作走与真人完全相同的协议。
- **硬约束**：
  1. 不改动任何后端 Go 代码；
  2. 不改动前端 JS；
  3. 不依赖服务侧机器人 `fillBots`，也不改变其既有逻辑；是否填充空位机器人仍由主桌第一位加入玩家决定；
  4. 现有服务端 exe 路径：`D:\SystemTools\talkcards-windows-amd64.exe`，无需重新编译 Go。

## 2. 总体架构

4 个 CommonJS 模块，运行在 Node 进程（非浏览器）：

| 文件 | 职责 |
|---|---|
| `ai-bot.js` | 主入口：WebSocket 客户端、登录/入座/准备/开局、轮转驱动、LLM 决策、聊天、断线重连、终局循环 |
| `ai-config.js` | 配置加载：内置默认值 ← `ai-config.json` ← 环境变量覆盖（优先级递增），绝不打印 apiKey |
| `ai-LLM.js` | OpenAI 兼容接口 `chat/completions`（内置 `fetch` + `AbortController` 超时 + 稳健 JSON 解析） |
| `ai-shape.js` | 牌型 / 压牌 / 候选 / 牌面文案，规则与 `backend/internal/card` 及前端 `js/parser.js` 保持一致 |

依赖：仅 Node 18+（用到内置 `fetch`/`AbortController`/`WebSocket`），**无需 `npm install`**（不依赖 `ws` 等第三方包）。

## 3. 快速开始

```powershell
# 1) 复制模板并填写配置
copy ai-bot\ai-config.example.json ai-bot\ai-config.json

# 2) 编辑 ai-config.json：填 LLM 的 baseUrl/apiKey/model，以及服务器 url、机器人名、座位
# 3) 运行（确认服务器已启动，默认 ws://127.0.0.1:8000/ws）
node ai-bot\ai-bot.js
```

环境变量可覆盖配置（优先级最高）：

| 环境变量 | 对应字段 |
|---|---|
| `TALKCARDS_WS_URL` | `server.url` |
| `OPENAI_BASE_URL` | `llm.baseUrl` |
| `OPENAI_API_KEY` | `llm.apiKey` |
| `OPENAI_MODEL` | `llm.model` |
| `TALKCARDS_BOT_NAME` | `bot.name` |
| `TALKCARDS_JOIN_MODE` | `bot.join.mode`（`fixed` / `quickJoin`） |
| `TALKCARDS_DESK_ID` | `bot.join.deskId` |
| `TALKCARDS_POS_ID` | `bot.join.posId` |
| `TALKCARDS_HOST_AUTOSTART` | `bot.host.autoStart`（`1`/`0`） |
| `TALKCARDS_HOST_FILLBOTS` | `bot.host.fillBots`（`1`/`0`） |

无 apiKey 时机器人仍可运行：出牌走**规则兜底**（确定性策略）、不主动发言。适合无 LLM 的规则回归测试。

## 4. 配置项说明（`ai-config.json`）

```jsonc
{
  "server": { "url": "ws://127.0.0.1:8000/ws" },       // http/https 自动转 ws/wss 并补 /ws
  "llm": {
    "baseUrl": "https://api.openai.com/v1",            // OpenAI 兼容 baseURL
    "apiKey": "",                                      // 不落地日志
    "model": "gpt-4o-mini",
    "timeoutMs": 30000,                                 // 单次 LLM 请求超时
    "temperature": 0.2,
    "jsonMode": true                                    // 请求 response_format=json_object；部分网关不支持可置 false
  },
  "bot": {
    "name": "AI阿花",
    "join": { "mode": "fixed", "deskId": 1, "posId": 3 }, // mode: "fixed" | "quickJoin"
    "autoPrepare": true,                                // 入座后自动 PREPARE
    "host": { "autoStart": true, "fillBots": false },   // 机器人为主持人时的开局策略
    "chat": { "onOwnTurn": true, "onOthersTurn": false, "minIntervalMs": 5000 },
    "decideTimeoutMs": 30000,                           // 决策兜底（实际由 llm.timeoutMs 控制）
    "reconnect": { "enabled": true, "maxDelayMs": 10000 }, // 见 §9「断线重连（暂缓）」
    "keepPlaying": true,                                // 终局后自动重新准备、等下一局
    "verbose": false
  }
}
```

## 5. 协议要点（与服务器通信）

单层信封：`{"type":"<事件名>","data":<载荷>}`（`data` 可缺省）。卡牌编码：`{"value":3..17,"type":0..3}`，value 3–10/J/Q/K=11–13、A=14、2=15、小王=16、大王=17；type 0♥1♦2♠3♣（大小王 type 恒为 0）。

### 5.1 主要事件

| 方向 | 事件 | 载荷 / 说明 |
|---|---|---|
| 发 | `LOGIN` | 纯用户名字符串 |
| 收 | `LOGIN_SUCCESS` / `LOGIN_FAIL{msg}` | 登录成功(20 桌) / 失败文案 |
| 发 | `SITDOWN{deskId,posId}` | 入座 |
| 收 | `SITDOWN_SUCCESS{deskId,posId,posInfos,hostPosId}` / `SITDOWN_ERROR{msg}` | `posInfos` 为 8 座 `{posId,state,userName,isBot,trustee}`（state 0空/1未准备/2已准备） |
| 发 | `QUICK_JOIN` | 收 `QUICK_JOIN{deskId,posId,success}`（仅返回空位，**不真正入座**，需再发 SITDOWN） |
| 发 | `PREPARE` / `CANCEL_PREPARE` | 收 `PREPARE_SUCCESS` |
| 发 | `HOST_START_GAME[ {fillBots:true} ]` | 主持人开局（可选填充机器人） |
| 收 | `POS_STATUS_CHANGE{posId,state,userName,isBot}` | 座位状态变化（对局结束 state=0） |
| 收 | `HOST_CHANGE{posId}` | 主持人变更（posId=-1 表示桌空） |
| 收 | `GAME_START{cards:[{id,cards,ht}]}` | **广播全部 8 家手牌**（Bug-for-bug）；机器人只取 `id==自己posId` 的手牌 |
| 收 | `SHOW_TOP_CARD{topCards,dizhuPosId,timeout}` | 先手座位 `dizhuPosId` |
| 收 | `CTX_PLAY_CHANGE{ctxData{len,key,type,cards,posId},sumFeng,tmpFeng,posId,isPass,clear}` | 轮转帧：`posId` 是轮到谁；`ctxData` 是桌面最近一手 |
| 发 | `PLAY_CARD` (牌数组) / `PLAY_CARD` (空数组=过牌) | 收 `PLAY_CARD_SUCCESS{data,tmpFeng,sumFeng}` / `PLAY_CARD_ERROR` |
| 发 | `USER_MESSAGE` (字符串) | 公开聊天，收 `USER_MESSAGE{type,posId,msg,id,time}` |
| 收 | `GAME_OVER{winner,loser,score,ratio}` | 终局 |
| 收 | `RECONNECT{...}` | 断线重连自动坐回（见 §9） |

### 5.2 轮转与“要压哪张牌”的判定（关键）

`CTX_PLAY_CHANGE` 一个帧既描述“最近一手”又告诉“现在轮到谁”：

- 帧内 `posId` = 当前轮到出牌的人；`posId === 自己` 才轮到机器人行动。
- `ctxData`（非过牌且有牌）描述桌面待压牌：`type`/`key`/`len` 经 `ai-shape.parseShape` 还原为 `{kind,rank,len}`。
- **过牌帧 `isPass=true` 时 `ctxData` 为空**，桌面待压牌沿用上一手，客户端须自行维护“当前待压牌”。
- **`clear=true`** 表示本圈已收牌 / 接风 / 新一轮自由首出，待压牌清空 → 自由出牌（`standingShape=null`）。

因此机器人在 `onCtxPlayChange` 维护两个状态：`standingShape`（待压牌型，null=自由首出）与 `standingPos`（最近一次有效出牌者的座位，用于判断队友/对手）。规则：
- `clear` → 清空待压牌；
- 非过牌且有 `ctxData.type` → 更新待压牌与出牌者；
- 过牌帧 → 两者保持。

`PLAY_CARD_ERROR` 多为越序（客户端轮转过期），服务器会重推轮转帧对齐，机器人不删手牌、静待下一个 `CTX_PLAY_CHANGE`。

## 6. 决策流程（LLM 驱动 + 规则兜底）

轮到机器人时（`posId===自己` 且 `phase==='playing'` 且非 busy）：

1. 组装 prompt（见 §7），调用 LLM，期望返回一个 JSON：
   ```json
   { "action": "play", "cards": [{"value":13},{"value":13}], "chat": "给队友的一句，可为空" }
   // 或
   { "action": "pass", "cards": [], "chat": "" }
   ```
2. **落定（concretize）**：只信任 `value`，同点牌在 6 副牌里任意花色可互换，从手牌中取对应点值的 n 张；要求所有 `value` 相同、张数 1–24、手牌里够。
3. **校验（ai-shape.canBeat）**：与待压牌比较是否合法能压；自由首出时任意合法牌型即可。
4. 合法 → 发对应牌；`action==="pass"` → 发空数组过牌。
5. **兜底**：LLM 未就绪 / 请求失败 / 返回不合法 / 压不住 → `ai-shape.findHintCards` 给确定性出牌（自由首出取最小整组；跟牌取最小能压组；无牌可压则过牌），保证不出越序、永不死锁。
6. 出牌成功 `PLAY_CARD_SUCCESS` 后从本地手牌移除 `lastSent`；过牌不删牌。

## 7. Prompt 设计

- **系统提示**：人设（名字、座位、队伍奇偶）+ 玩法规则（牌型、压牌、分牌、王炸、接风、胜负）+ 输出 JSON 格式要求。
- **用户上下文**（每回合）：手牌摘要（`3×2，10×3，K×1，大王×2`）、桌面待压牌（牌型 + 最近出牌者队友/对手）、滚动分 `tmpFeng`、各座已收分、本队/对手已收分合计、最近公开聊天记录。
- **聊天（他人回合，可选）**：给“轮到 X 号（队友/对手）”的简短观战上下文，LLM 输出 `{"chat":"一句话或空"}`，只在有帮助时发言，`minIntervalMs` 节流。

要点：同点牌只需填 `value`（type 任意），避免花色写错被服务器“所出牌不在手上”拒绝。

## 8. 主持人与开局

- 第一个入座者成为主持人（`SITDOWN_SUCCESS.hostPosId` 为 -1，随后收到 `HOST_CHANGE` 广播才更新为真实值；持续用 `HOST_CHANGE` 跟踪）。
- 机器人仅当自己是主持人且 `host.autoStart=true` 才考虑开局：
  - **8 人坐满且全部已准备** → 发 `HOST_START_GAME`（无空位，无需 fillBots）；
  - 有空位且 `host.fillBots=true` 且“非空座位全部已准备” → 发 `HOST_START_GAME{fillBots:true}`（补机器人，仍不修改服务端 fillBots 逻辑）。
- 非主持人始终等待真人主持人开局，绝不擅自发 `HOST_START_GAME`。

## 9. 断线重连（暂缓，勿深入）

> **状态：暂缓。** 现有代码已有一版基础实现（`onDisconnected` 指数退避重连、游戏中掉线靠服务器 `RECONNECT` + 重放 `GAME_START`/轮转帧自动坐回、无 Seat 保留则退化为重新入座），但**未做实机验证**，本轮不继续迭代。后续接盘时需重点核对：
> - 游戏中掉线 → 服务器保留座位（`reconnect-timeout` 内）→ 重连后 `RECONNECT` + `replayGameFrames` 重放在不同 tick，需显式消费重放帧刷新 `standingShape`/手牌；
> - 全员掉线/超时 → 对局终止，无 `RECONNECT`，需 5s 兜底回退到 `freshJoin`；
> - 重连后手牌以服务器重放的 `GAME_START` 为准（`takeCards` 对齐）。

## 10. 已验证 / 未验证与接盘 TODO

**已验证**：4 文件 `node --check` 语法通过；`ai-shape.js` 规则单测通过；连真实 exe（端口 8766）冒烟：登录 → 入座 → 准备 正常。

**未验证 / TODO**（接盘优先级建议）：
1. ~~断线重连~~（已暂缓，见 §9）
2. 连真实 LLM endpoint + key，验证 `decide`/`comment` 的 prompt 与 JSON 解析、温度与 `jsonMode` 取舍；
3. 完整一局回归：用 8 个 `e2e-bot.js` 机器人同桌 + 1 个无 LLM 的 ai-bot，验证它在整局中持续出牌/过牌、收分、终局循环（规则兜底路径，不依赖 LLM）；
4. LLM 卡牌点值选择的团队配合质量、`onOthersTurn` 发言频率与内容是否合适（需人工体验调 prompt）。

## 11. 相关规则与协议源文件（改规则需同步）

- 玩法规则：仓库根 `game-rules.md`
- 压牌/牌型规则：`backend/internal/card/shape.go`、`card.go`、`dict.go`；前端 `backend/web/static/js/parser.js`
- 事件契约：`backend/internal/hub/events.go`
- 服务端机器人策略（兜底参考）：`backend/internal/bot/bot.go`、`static.go`
- 协议验收参考：`backend/scripts/e2e-bot.js`（WsClient 的 pending 缓冲 / tryPlay 铁律）、`e2e-audit.js`