# AI 沟通牌机器人（LLM 驱动的真人玩家）

本目录提供一个以 LLM 驱动决策的「真人玩家」机器人，作为**普通 WebSocket 客户端**接入现有 Go 服务器，与真人玩家完全同构地参与对局与公开聊天。除基本的登录/入座/出牌外，还实现了**协同作战**：盘面记忆（记牌/推断）、团队共识（plan）、跨回合协商线（队友问答/指令/建议/防刷屏）。

---

## 1. 目标与硬约束

- 在 Go 服务器之上，提供一个 LLM 驱动决策的 AI 真人玩家；登录、坐下、出牌、过牌、聊天收发等操作走与真人完全相同的协议。
- **硬约束**：
  1. 不改动任何后端 Go 代码；
  2. 不改动前端 JS；
  3. 不依赖服务侧机器人 `fillBots`，也不改变其既有逻辑；是否填充空位机器人仍由主桌第一位加入玩家决定；
  4. 现有服务端 exe 路径：`D:\SystemTools\talkcards-windows-amd64.exe`，无需重新编译 Go。

---

## 2. 总体架构

5 个 CommonJS 模块，运行在 Node 进程（非浏览器）：

| 文件 | 职责 |
|---|---|
| `ai-bot.js` | 主入口：WebSocket 客户端、登录/入座/准备/开局、轮转驱动、LLM 决策、协同协商线（问答/指令/建议）、聊天、断线重连、终局循环 |
| `ai-brain.js` | 盘面记忆与推断：记牌、库存/残牌估算、压牌能力评估（纯确定性，不占 LLM token） |
| `ai-config.js` | 配置加载：内置默认值 ← `ai-config.json` ← 环境变量覆盖（优先级递增），绝不打印 apiKey |
| `ai-LLM.js` | OpenAI 兼容接口 `chat/completions`（内置 `fetch` + `AbortController` 超时 + 稳健 JSON 解析） |
| `ai-shape.js` | 牌型 / 压牌 / 候选 / 牌面文案 / 点值名解析，规则与 `backend/internal/card` 及前端 `js/parser.js` 保持一致 |

依赖：仅 Node 18+（用到内置 `fetch`/`AbortController`/`WebSocket`），**无需 `npm install`**（不依赖 `ws` 等第三方包）。

---

## 3. 快速开始

```powershell
# 1) 复制模板并填写配置
copy ai-bot\ai-config.example.json ai-bot\ai-config.json

# 2) 编辑 ai-config.json：填 LLM 的 baseUrl/apiKey/model，以及服务器 url、机器人名、座位
#    （多 bot 用顶层 "bots":[ {...}, {...} ] 数组，每项一个机器人，详见 §4）
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
| `TALKCARDS_BOT_NAME` | `bot.name`（仅单 bot 模式） |
| `TALKCARDS_JOIN_MODE` | `bot.join.mode`（仅单 bot 模式） |
| `TALKCARDS_DESK_ID` | `bot.join.deskId`（仅单 bot 模式） |
| `TALKCARDS_POS_ID` | `bot.join.posId`（仅单 bot 模式） |
| `TALKCARDS_HOST_AUTOSTART` | `bot.host.autoStart`（仅单 bot 模式） |
| `TALKCARDS_HOST_FILLBOTS` | `bot.host.fillBots`（仅单 bot 模式） |

无 apiKey 时机器人仍可运行：出牌走**规则兜底**（确定性策略）、协同链路用**规则分类 + 模板措辞**（不依赖 LLM）。适合无 LLM 的规则回归测试。

---

## 4. 配置项说明（`ai-config.json`）

**多 bot 模式**（推荐）：顶层 `bots` 为数组，每项一个机器人；`server` / `llm` 为所有 bot 共享。每项只写要覆盖的字段即可，其余沿用内置默认值：

```jsonc
{
  "server": { "url": "ws://127.0.0.1:8000/ws" },       // http/https 自动转 ws/wss 并补 /ws
  "llm": {
    "baseUrl": "https://api.openai.com/v1",
    "apiKey": "",
    "model": "gpt-4o-mini",
    "timeoutMs": 30000,
    "temperature": 0.2,
    "jsonMode": true
  },
  "bots": [
    { "name": "AI阿花", "join": { "mode": "fixed", "deskId": 1, "posId": 0 }, "host": { "autoStart": true, "fillBots": true }, "verbose": true },
    { "name": "AI阿强", "join": { "mode": "fixed", "deskId": 1, "posId": 2 } }
  ]
}
```

`bots[i]` 的字段（均为可选，缺省用默认值，见下）：

| 字段 | 默认 / 说明 |
|---|---|
| `name` | 机器人用户名（须全局唯一、非空、≤10 字） |
| `join.mode` | `"fixed"`（指定桌号座位）\| `"quickJoin"`（自动找空位） |
| `join.deskId` / `join.posId` | `mode:"fixed"` 时的桌号（1-20）与座位（0-7） |
| `autoPrepare` | `true`：入座后自动 PREPARE |
| `host.autoStart` / `host.fillBots` | 自己是主持人时的开局策略 |
| `chat.onOwnTurn` / `chat.onOthersTurn` / `chat.minIntervalMs` | 发言时机与节流 |
| `reconnect.{enabled,maxDelayMs}` | 断线重连（§9，暂缓） |
| `keepPlaying` | `true`：终局后自动重新准备等下一局 |
| `verbose` | `true`：打印调试日志（多 bot 时日志带 `[ai-bot:名字]` 前缀） |
| `brain.handInference` | `"estimate"`（默认）：只用公开信息+残牌估算对手手牌 \| `"full"`：读 GAME_START 广播的全量手牌（精准） |
| `qa.enabled` | `true`：开启队友问答/协同（建议/指令/群发/点名） |
| `qa.minIntervalMs` / `qa.maxReplyPerMin` | 同 bot 最短发言间隔 / 每分钟最大回复条数（防刷屏） |
| `qa.groupSilenceMs` | 群发问题无人回应时，上一个队友兜底回答的等待时长 |
| `strategy.finishRemainLte` / `blockRemainLte` / `scoreNearWin` | 战略发言阈值：队友/自己剩牌 ≤N 喊放行接风、对手剩牌 ≤N 喊压制、完成者收分 ≥N 喊接近胜负线（默认 2/2/240） |

**单 bot（向后兼容）**：无 `bots` 数组时，沿用旧式顶层 `bot` 字段：

```jsonc
{
  "server": { "url": "ws://127.0.0.1:8000/ws" },
  "llm": { "baseUrl": "https://api.openai.com/v1", "apiKey": "", "model": "gpt-4o-mini",
           "timeoutMs": 30000, "temperature": 0.2, "jsonMode": true },
  "bot": {
    "name": "AI阿花",
    "join": { "mode": "fixed", "deskId": 1, "posId": 3 }, // mode: "fixed" | "quickJoin"
    "autoPrepare": true,
    "host": { "autoStart": true, "fillBots": false },
    "chat": { "onOwnTurn": true, "onOthersTurn": false, "minIntervalMs": 5000 },
    "reconnect": { "enabled": true, "maxDelayMs": 10000 },
    "keepPlaying": true,
    "verbose": false
  }
}
```

---

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
| 收 | `CTX_PLAY_CHANGE{ctxData{len,key,type,cards,posId},sumFeng,tmpFeng,posId,isPass,clear,replay}` | 轮转帧：`posId` 是轮到谁；`ctxData` 是桌面最近一手 |
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

因此机器人在 `onCtxPlayChange` 维护两个状态：`standingShape`（待压牌型，null=自由首出）与 `standingPos`（最近一次有效出牌者的座位，用于判断队友/对手）。规则：`clear` → 清空待压牌；非过牌且有 `ctxData.type` → 更新待压牌与出牌者；过牌帧 → 两者保持。

`PLAY_CARD_ERROR` 多为越序（客户端轮转过期），服务器会重推轮转帧对齐，机器人不删手牌、静待下一个 `CTX_PLAY_CHANGE`。

### 5.3 记账与协议事实（盘面记忆依赖）

- `CTX_PLAY_CHANGE.ctxData.cards` 为**最近一手实际打出的牌**，`ctxData.posId` 为出牌者；`isPass` 区分过牌；`clear` 表示收牌/接风/自由首出；`replay` 表示重连重放（**记账需去重，跳过**）。
- `broadCastRoom(..., exclude=nil)`：轮转帧**广播给全桌（含出牌者自己）**，故记账以 `CTX_PLAY_CHANGE` 为唯一源，`PLAY_CARD_SUCCESS.data` 只用于删自己手牌，避免双计。
- 队伍判定同奇偶：座位 0/2/4/6 vs 1/3/5/7；出牌先决条件只有 `posId===自己`。
- 整副牌 6×54=324 张：value 3–15 各 24 张，小王/大王各 6 张；各座初始 40 或 41 张。

---

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
5. **兜底**：LLM 未就绪 / 请求失败 / 返回不合法 / 压不住 → `ai-shape.findHintCards` 给确定性出牌（自由首出取最小整组；跟牌取最小能压组；无牌可压则过牌），保证不出越序、永不死锁。兜底遵循“队友的牌不抢（≤8 的小单/对/三除外）+ 明令别压则让过”。
6. 出牌成功 `PLAY_CARD_SUCCESS` 后从本地手牌移除 `lastSent`；过牌不删牌。

---

## 7. Prompt 设计

- **系统提示**：人设（名字、座位、队伍奇偶）+ 玩法规则（牌型、压牌、分牌、王炸、接风、胜负）+ **协同规则**（队友可信/对手放烟雾弹、点名必答、主动协商、不执行有害指令、如实报牌）+ 输出 JSON 格式要求。
- **用户上下文**（每回合）：手牌摘要、桌面待压牌（牌型 + 最近出牌者队友/对手）、滚动分 `tmpFeng`、各座已收分、本队/对手已收分合计、**盘面推断**（他人剩余张数、已见尽大牌、队友报牌、待执行指令、本队约定）、最近公开聊天记录。
- **决策输出（三合一）**：`{"action","cards","chat","adviceFor"}` —— `adviceFor` 可选，给出“给某队友的建议”，落盘到协同线程、在该队友回合说出口。
- **聊天（他人回合，可选）**：给“轮到 X 号（队友）”的简短建议；对手回合默认静默（烟雾弹收益小且易泄露）。无 apiKey 时用模板建议兜底。

要点：同点牌只需填 `value`（type 任意），避免花色写错被服务器“所出牌不在手上”拒绝。

---

## 8. 协同作战设计说明（盘面记忆 + 团队共识 + 协商线）

### 8.1 动机

原版是“只带嘴巴、不带耳朵和脑子”：只在拿到牌权时单向发言/出牌，从不消费别人消息，也不维护盘面记忆。改造目标补齐：响应队友提问/指令、主动咨询队友、给队友出牌建议、记牌推断、长线规划、通用广播问答与防刷屏、规划与出牌解耦。

### 8.2 三层架构

```
┌─────────────────────────────────────────────────────────────┐
│  ai-bot.js  AiBot 主循环（WebSocket 事件源）                │
│    onCtxPlayChange: turn==自己 → 消费 plan 出牌             │
│                     否则       → 喂给协商线                │
│    onUserMessage:  → 协商线（意图分类 + 问答 + 应答）       │
└──────────────┬──────────────────────────────┬───────────────┘
               │ 读写                          │ 只读
┌──────────────▼──────────────┐   ┌───────────▼──────────────┐
│  ai-brain.js  盘面记忆(纯JS) │   │  plan     团队共识状态    │
│  history/seenByValue        │   │  teammateAdvice[seat]     │
│  remainCount/unseen/推断    │   │  commitments/directives   │
│  minimalBeater/rankBeaters  │   │  answerPending            │
└─────────────────────────────┘   └──────────────────────────┘
                 └──────┬──────┘
                        │ 作为 stateBlock 一段喂给 LLM
                 ┌──────▼──────┐
                 │   LLM 决策  │  decide / 建议 / 应答措辞
                 └─────────────┘
```

### 8.3 模块一：盘面记忆 `ai-brain.js`

纯确定性（不依赖 LLM、不碰 socket）：

- `history[]`：每手 `{posId, cards, isPass, ts}`，来源 `CTX_PLAY_CHANGE.ctxData`，`replay` 帧去重，`onGameStart` 清空重建。
- `seenByValue`：全桌按点值已公开打出的牌数。
- `baseline[8]` / `remainCount[8]`：开局各座张数 / 剩余张数（= baseline − 已出）。
- `unseen[v]`：`库存(v) − 已见(v) − 自己手牌(v)`；`==0` 说明对手不可能再持有该点值。
- `inferenceSummary()`：把残牌/已见尽点值转成中文一句供 prompt；`full` 模式额外输出对手精确残牌。
- `minimalBeater(hand, standing)` / `rankBeaters(...)`：求最小能压组 / 按代价升序排出能压者（供问答与选人）。

**记账要点**：牌以 `value` 为主键计数（同点跨 6 副可互换，`type` 不参与计数，仅展示）；`PLAY_CARD_SUCCESS` 只删自己手牌，别人牌（含自己的，服务器广播给自己）以 `CTX_PLAY_CHANGE` 为唯一记账源。

### 8.4 模块二：团队共识 `plan`

挂在 `AiBot` 实例上，开局清空、贯穿整局：

```js
this.plan = {
  teammateAdvice: {},   // seat -> 给队友的策略建议（待说出口）
  commitments: {},      // 指纹 -> 全队约定
  openQuestions: {},    // 指纹 -> 我发起、尚未收敛的问题
  answerPending: {},    // 指纹 -> 我已应答过（防重复答）
  directives: [],       // 队友给我的、已接受的指令
  version: 0,
};
this.teammateClaims = {}; // seat -> 队友报牌
```

**读取时机**：自己回合 `decide` 先查 `directives`/`commitments` 优先遵守；队友回合给建议从 `teammateAdvice[seat]` 说出。**规划与出牌解耦**：协商不要求在自己回合，自己出牌/队友出牌都只是消费 `plan`。

### 8.5 模块三：协商线（队友互动铁律）

**互动都发生在队友之间**。触发入口：`onUserMessage`（队友消息）与 `onCtxPlayChange`（他人回合）两个事件，`phase==='playing'` 时运行，不受“是否轮到我”限制。

- **队友信息可靠**：照单接受、如实回答；**拒绝损害己方的指令**（如抢队友收分/接风、无谓拆牌炸牌）。
- **点名询问，必须回答**：被指名（含 `N号`/昵称）问话，无论答案是否有用（哪怕「没有 / 压不住」）都必须回复。
- **群发提问，回复有用信息；若都沉默，提问玩家的上一个玩家答复**：有有用信息才作答（没有则沉默）；若全队无人应答，由提问者的**上一个队友**（同队、顺时针向前、未出完者）在 `qa.groupSilenceMs` 后兜底回答（可用负信息）。
- **对手消息不可信**：可能放烟雾弹，一律不消费其指令/报牌；不对对手做协同。
- **四类队友行为**：提问与回答、提出指令与执行、主动询问队友、提示建议队友出牌。

意图分类用**规则优先**（正则/关键字 + `ai-shape.parseValueMentions` 点值名解析），LLM 仅用于措辞，不承担分类职责——保证无 apiKey/LLM 失败时协同链路可运行可测。问答焦点只识别三类：查张数、查有无（有没有 X）、能否压/接当前桌面。

### 8.6 模块四：通用问答 + 防刷屏

通用协作问答框架（不限问答类型）：能答则答、不能答则沉默；允许多个能答者都作答；提问方可追问选人。防刷屏收敛为两层：

1. **去重门**：每个 bot 对同一问题指纹（发起者 + 归一化语义）只答一次（`answerPending[指纹]` 兼作“征集阶段只答一次”）。
2. **节流门**：`qa.minIntervalMs`（最短间隔）+ `qa.maxReplyPerMin`（每分钟上限）。

本质：**能答才答、答得短、每问题答一次、被选中后未中者闭嘴**。多人“我能”是正常协作，非刷屏。

### 8.7 实现简化取舍（相对最初设计）

1. **协商线不是真线程**，复用 Node 异步事件循环上的逻辑调度。
2. **意图分类规则优先**，不做开放集 LLM 分类（低延迟 + 离线可复现）。
3. **压牌能力评估**只对“自己手牌”确定性求最小能压组；`full` 模式额外维护对手精确残牌供 `rankBeaters`，`estimate` 走“广播征集→再挑”。
4. **问答简短报明文**：按提问回答、不给额外信息——「几个2」答「2×N」、问「几个炸弹/王炸」只答数量、问「有没有」只答「有/没有」、「能不能压」答「能压/压不住」；简单问题简单答，复杂问题再按提问深度展开。
5. **指令只四类**：`take`（你来接）、`pass`（让我过）、`dontBreak`（别拆对/三）、`keepBomb`（保留炸弹）；“损害己方”用确定性规则判断——`take` 且桌面是队友的牌判为有害拒绝。
6. **防刷屏收敛为两层**（见 §8.6）；原“相关性门”已并入意图分类（对手/自己/SYS 直接忽略）。

队友问答简短报明文（不再有披露尺度）：问什么答什么、不改答额外信息。

### 8.8 发言策略（减少无用信息 + 战略判断）

- **回合陈述性发言默认静默**：自己出牌不再附带“我出XX”之类的陈述；LLM 的 `chat` 仅在是提问 / 指令 / 点名协调时才说出口（`emitOwnTurnChat`），普通一手牌一律留空。
- **队友提问/指令优先级最高**：点名提问必答、指令必回，走 `priorityChat` 绕过节流（仍受 `qa.enabled` 与 `answerPending` 去重约束），保证快速响应。
- **问答简短报明文**：`几个2`→`2×N`、`几个炸弹/王炸`→只答数量、`有没有`→`有/没有`、`能不能压`→`能压/压不住`；简单问题简单答，不给额外信息。
- **胜负/关键胜负手即时提出**（`strategicSignal`，确定性、每局面只广播一次）：①自己/队友剩牌 ≤ `finishRemainLte` → 喊放行+接风；②对手剩牌 ≤ `blockRemainLte` → 喊压制别放走；③本队完成者收分 ≥ `scoreNearWin` → 喊接近胜负线。由 `emitStrategic` 在任何轮次（自己/队友/对手）即时发出，不受 `chat.onOwnTurn/onOthersTurn` 开关限制。

---

## 9. 主持人与开局

- 第一个入座者成为主持人（`SITDOWN_SUCCESS.hostPosId` 为 -1，随后收到 `HOST_CHANGE` 广播才更新为真实值；持续用 `HOST_CHANGE` 跟踪）。
- 机器人仅当自己是主持人且 `host.autoStart=true` 才考虑开局：
  - **8 人坐满且全部已准备** → 发 `HOST_START_GAME`（无空位，无需 fillBots）；
  - 有空位且 `host.fillBots=true` 且“非空座位全部已准备” → 发 `HOST_START_GAME{fillBots:true}`（补机器人，仍不修改服务端 fillBots 逻辑）。
- 非主持人始终等待真人主持人开局，绝不擅自发 `HOST_START_GAME`。

---

## 10. 断线重连（暂缓，勿深入）

> **状态：暂缓。** 现有代码已有一版基础实现（`onDisconnected` 指数退避重连、游戏中掉线靠服务器 `RECONNECT` + 重放 `GAME_START`/轮转帧自动坐回、无 Seat 保留则退化为重新入座），但**未做实机验证**，本轮不继续迭代。后续接盘时需重点核对：
> - 游戏中掉线 → 服务器保留座位（`reconnect-timeout` 内）→ 重连后 `RECONNECT` + `replayGameFrames` 重放在不同 tick，需显式消费重放帧刷新 `standingShape`/手牌；
> - 全员掉线/超时 → 对局终止，无 `RECONNECT`，需 5s 兜底回退到 `freshJoin`；
> - 重连后手牌以服务器重放的 `GAME_START` 为准（`takeCards` 对齐）。

---

## 11. 已验证 / 未验证与接盘 TODO

**已验证**：5 文件 `node --check` 语法通过；`ai-brain` 记账/推断抽测通过；协同链路 5 用例冒烟通过（点名必答、压牌判断、有害指令拒绝、对手消息忽略、群发沉默兜底）；连真实 exe（端口 8766）冒烟：登录 → 入座 → 准备 正常。

**未验证 / TODO**（接盘优先级建议）：
1. ~~断线重连~~（已暂缓，见 §10）
2. 连真实 LLM endpoint + key，验证 `decide`/建议的 prompt 与 JSON 解析、温度与 `jsonMode` 取舍；
3. 完整一局回归：用 8 个 `e2e-bot.js` 机器人同桌 + 1 个无 LLM 的 ai-bot，验证它在整局中持续出牌/过牌、收分、终局循环（规则兜底路径，不依赖 LLM）；
4. 多 bot 桌实连回归：`node ai-bot/ai-bot.js`（多 bot 配置）连真实 exe，重点观察刷屏抑制、协作建议是否被队友消费、终局策略收敛，调 `strategy`/`qa` 阈值与 prompt 语气；
5. LLM 卡牌点值选择的团队配合质量（需人工体验调 prompt）。

---

## 12. 相关规则与协议源文件（改规则需同步）

- 玩法规则：仓库根 `game-rules.md`
- 压牌/牌型规则：`backend/internal/card/shape.go`、`card.go`、`dict.go`；前端 `backend/web/static/js/parser.js`
- 事件契约：`backend/internal/hub/events.go`
- 服务端机器人策略（兜底参考）：`backend/internal/bot/bot.go`、`static.go`
- 协议验收参考：`backend/scripts/e2e-bot.js`（WsClient 的 pending 缓冲 / tryPlay 铁律）、`e2e-audit.js`