// ai-bot.js LLM 驱动的沟通牌真人玩家（普通 WebSocket 客户端接入）。
// 与真人完全同构：登录 / 入座 / 准备 / 出牌 / 过牌 / 公开聊天 / 断线重连。
// 决策由 LLM 给出，ai-shape 校验，非法或 LLM 失败时回退到确定性策略，绝不卡死。
// 协同作战：ai-brain 记牌/推断，plan 团队共识，协商线程做主动问答/建议/指令（跨回合）。
// 不改动任何后端 Go / 前端 JS，不依赖服务端 fillBots。
'use strict';

const { loadConfigs } = require('./ai-config');
const LLM = require('./ai-LLM');
const shape = require('./ai-shape');
const { Brain } = require('./ai-brain');

// 纯 WebSocket 客户端：帧到达时若没有对应监听器则先缓冲（背靠背多帧同一 tick）。
class WsClient {
  constructor(url) {
    this.handlers = new Map();
    this.pending = new Map();
    this.queue = [];
    this.url = url;
    this.closed = false;
    this.ws = null;
  }
  open() {
    this.ws = new WebSocket(this.url);
    this.ws.onopen = () => { while (this.queue.length) this.ws.send(this.queue.shift()); };
    this.ws.onmessage = (ev) => {
      let m;
      try { m = JSON.parse(ev.data); } catch (e) { return; }
      if (!m || !m.type) return;
      const set = this.handlers.get(m.type);
      if (set && set.size) {
        [...set].forEach((fn) => fn(m.data));
      } else {
        const buf = this.pending.get(m.type) || [];
        if (buf.length < 200) buf.push(m.data);
        this.pending.set(m.type, buf);
      }
    };
    this.ws.onerror = () => { /* onclose 总会随后触发 */ };
    this.ws.onclose = () => { this.onClose && this.onClose(); };
  }
  on(ev, fn) {
    if (!this.handlers.has(ev)) this.handlers.set(ev, new Set());
    this.handlers.get(ev).add(fn);
    return this;
  }
  off(ev, fn) { const s = this.handlers.get(ev); if (s) s.delete(fn); }
  emit(ev, data) {
    const msg = { type: ev };
    if (data !== undefined) msg.data = data;
    const s = JSON.stringify(msg);
    if (this.ws && this.ws.readyState === 1) this.ws.send(s);
    else this.queue.push(s);
  }
  close() {
    this.closed = true;
    if (this.ws) this.ws.close();
  }
}

class AiBot {
  constructor(cfg) {
    this.cfg = cfg;
    this.sock = null;
    this.posId = -1;
    this.deskId = -1;
    this.hostPosId = -1;
    this.dizhuPosId = -1;
    this.hand = [];                 // [{value,type}]
    this.seats = {};                // posId -> {state,userName,isBot,trustee}
    this.phase = 'lobby';           // lobby | playing | over
    this.busy = false;
    this.standingShape = null;      // 桌面待压牌型（null = 自由首出）
    this.standingPos = null;        // 最后一位有效出牌者（队友/对手判断用）
    this.tmpFeng = 0;
    this.sumFeng = {};              // posId -> 已收分
    this.chatLog = [];              // [{posId,name,msg}]
    this.lastSent = null;           // 最近一次 PLAY_CARD 发出的牌
    this.lastCommentAt = 0;         // 他人回合发言节流时间戳
    this.retry = 0;
    this.hostStarted = false;
    this.stopping = false;
    this.reconnectTimer = null;
    this.reconnectWatch = null;
    this.expectReconnect = false;
    this.quickJoinRetrying = false;

    // 协同作战新增状态。
    this.brain = new Brain();       // 盘面记忆（记牌/推断）
    this.initPlan();
    this.lastChatAt = 0;            // 协商发言节流时间戳
    this.qaReplyTimes = [];         // 每分应答时间戳（防刷屏）
    this.pendingGroup = null;       // 群发问题兜底观察器
  }

  initPlan() {
    this.plan = {
      teammateAdvice: {},   // seat -> 我给队友的策略建议（待说出口）
      commitments: {},      // 指纹 -> {text,kind,posId} 全队约定
      openQuestions: {},    // 指纹 -> true 我发起的、尚未收敛的问题
      answerPending: {},    // 指纹 -> true 我已应答过的问题（防重复答）
      directives: [],       // 队友给我的、已接受的指令（约束我方决策）
      version: 0,
    };
    this.teammateClaims = {}; // seat -> {text,values} 队友报牌
    this.pendingGroup = null;
    this.qaReplyTimes = [];
    this.lastChatAt = 0;
  }

  log(...args) { console.log('[ai-bot:' + this.cfg.bot.name + ']', ...args); }
  dbg(...args) { if (this.cfg.bot.verbose) console.log('[ai-bot:' + this.cfg.bot.name + ']', ...args); }

  start() {
    this.attachSocket();
    this.connect();
    this.registerSignalHandlers();
  }

  attachSocket() {
    this.sock = new WsClient(this.cfg.server.url);
    const s = this.sock;
    s.onClose = () => this.onDisconnected();
    s.on('LOGIN_SUCCESS', (d) => this.onLoginSuccess(d));
    s.on('LOGIN_FAIL', (d) => this.onLoginFail(d));
    s.on('SITDOWN_SUCCESS', (d) => this.onSitdownSuccess(d));
    s.on('SITDOWN_ERROR', (d) => this.onSitdownError(d));
    s.on('QUICK_JOIN', (d) => this.onQuickJoin(d));
    s.on('POS_STATUS_CHANGE', (d) => this.onPosStatusChange(d));
    s.on('POS_STATUS_RESET', (d) => this.onPosStatusReset(d));
    s.on('HOST_CHANGE', (d) => this.onHostChange(d));
    s.on('PREPARE_SUCCESS', () => this.onPrepareSuccess());
    s.on('RECONNECT', (d) => this.onReconnect(d));
    s.on('GAME_START', (d) => this.onGameStart(d));
    s.on('SHOW_TOP_CARD', (d) => this.onShowTopCard(d));
    s.on('CTX_PLAY_CHANGE', (d) => this.onCtxPlayChange(d));
    s.on('PLAY_CARD_SUCCESS', (d) => this.onPlayCardSuccess(d));
    s.on('PLAY_CARD_ERROR', () => this.onPlayCardError());
    s.on('USER_MESSAGE', (d) => this.onUserMessage(d));
    s.on('GAME_OVER', (d) => this.onGameOver(d));
    s.on('MESSAGE', (d) => this.onMessage(d));
  }

  connect() {
    this.sock.open();
    this.sock.emit('LOGIN', this.cfg.bot.name);
  }

  registerSignalHandlers() {
    const shutdown = () => {
      if (this.stopping) return;
      this.stopping = true;
      this.log('收到退出信号，正在退出……');
      this.sock.close();
      process.exit(0);
    };
    process.on('SIGINT', shutdown);
    process.on('SIGTERM', shutdown);
  }

  // ---- 大厅 / 入座 / 准备 / 开局 ----

  onLoginSuccess() {
    this.retry = 0;
    this.log('登录成功：', this.cfg.bot.name);
    if (!this.cfg.llm.apiKey) {
      this.log('未配置 LLM apiKey，将使用规则兜底策略出牌、且不主动发言（可设置 OPENAI_API_KEY 或 ai-config.json）');
    }
    // 游戏中掉线后的重连：服务器会自动坐回并重放，无需重新入座。
    if (this.expectReconnect) {
      this.expectReconnect = false;
      this.log('等待服务器重连续局（服务器会推送 RECONNECT + 重放帧）……');
      this.reconnectWatch = setTimeout(() => {
        // 兜底：对局若已终止、服务器不重连，则退回到正常入座。
        if (this.phase !== 'playing') this.freshJoin();
      }, 5000);
      return;
    }
    this.freshJoin();
  }

  freshJoin() {
    if (this.cfg.bot.join.mode === 'quickJoin') {
      this.emitQuickJoin();
    } else {
      const { deskId, posId } = this.cfg.bot.join;
      this.log('入座 桌', deskId, '座', posId);
      this.sock.emit('SITDOWN', { deskId, posId });
    }
  }

  onLoginFail(d) {
    this.log('登录失败：', d && d.msg);
    this.stopping = true;
    process.exit(1);
  }

  emitQuickJoin() {
    if (this.quickJoinRetrying) return;
    this.quickJoinRetrying = true;
    this.sock.emit('QUICK_JOIN');
  }

  onQuickJoin(d) {
    this.quickJoinRetrying = false;
    if (d && d.success) {
      this.log('快速入座 桌', d.deskId, '座', d.posId);
      this.sock.emit('SITDOWN', { deskId: d.deskId, posId: d.posId });
    } else {
      this.log('暂无空位，3 秒后重试快速入座……');
      setTimeout(() => this.emitQuickJoin(), 3000);
    }
  }

  onSitdownSuccess(d) {
    this.posId = d.posId;
    this.deskId = d.deskId;
    if (typeof d.hostPosId === 'number') this.hostPosId = d.hostPosId;
    (d.posInfos || []).forEach((p) => this.updateSeat(p));
    this.log('已入座 桌', this.deskId, '座', this.posId, '（主持人座位', this.hostPosId, '）');
    if (this.cfg.bot.autoPrepare) this.sock.emit('PREPARE');
  }

  onSitdownError(d) {
    this.log('入座失败：', d && d.msg);
    // 位置被占：快速入座模式下重新找位；固定模式则稍后重试。
    if (this.cfg.bot.join.mode === 'quickJoin') {
      setTimeout(() => this.emitQuickJoin(), 2000);
    } else {
      this.log('3 秒后重试固定入座……');
      setTimeout(() => {
        const { deskId, posId } = this.cfg.bot.join;
        this.sock.emit('SITDOWN', { deskId, posId });
      }, 3000);
    }
  }

  onReconnect(d) {
    if (this.reconnectWatch) { clearTimeout(this.reconnectWatch); this.reconnectWatch = null; }
    this.deskId = d.deskId;
    this.posId = d.posId;
    if (typeof d.hostPosId === 'number') this.hostPosId = d.hostPosId;
    (d.posInfos || []).forEach((p) => this.updateSeat(p));
    this.log('断线重连，坐回 桌', this.deskId, '座', this.posId);
  }

  onHostChange(d) {
    if (d && typeof d.posId === 'number') this.hostPosId = d.posId;
  }

  onPosStatusChange(d) {
    if (!d) return;
    const prev = this.seats[d.posId] || {};
    this.seats[d.posId] = {
      state: d.state != null ? d.state : prev.state,
      userName: d.userName !== undefined ? d.userName : prev.userName,
      isBot: d.isBot != null ? d.isBot : prev.isBot,
    };
    this.dbg('座位状态：', d.posId, this.seats[d.posId]);
    this.maybeHostStart();
  }

  onPosStatusReset(d) {
    (d.pos || []).forEach((p) => this.updateSeat(p));
  }

  updateSeat(p) {
    if (!p) return;
    this.seats[p.posId] = { state: p.state, userName: p.userName, isBot: !!p.isBot, trustee: !!p.trustee };
  }

  onPrepareSuccess() {
    this.log('已准备');
    this.updateSeat({ posId: this.posId, state: 2, userName: this.cfg.bot.name });
    this.maybeHostStart();
  }

  maybeHostStart() {
    if (this.hostStarted) return;
    if (this.phase !== 'lobby') return;
    if (this.hostPosId !== this.posId) return; // 只有主持人位
    if (!this.cfg.bot.host.autoStart) return;

    // 收集当前座位状态，判断是否可开局。
    const states = [];
    for (let i = 0; i < 8; i++) states.push(this.seats[i] ? this.seats[i].state : 0);
    const filled = states.filter((s) => s === 1 || s === 2).length;
    const empty = states.filter((s) => s === 0).length;

    if (empty === 0 && filled === 8 && states.every((s) => s === 2)) {
      // 8 人坐满且全部准备
      this.log('8 人坐满且全部准备，主持人开局');
      this.hostStarted = true;
      this.sock.emit('HOST_START_GAME');
      return;
    }
    if (empty > 0 && this.cfg.bot.host.fillBots && states.every((s) => s === 0 || s === 2)) {
      // 有空位，但允许填充机器人补位
      this.log('有', empty, '个空位，按配置填充机器人开局');
      this.hostStarted = true;
      this.sock.emit('HOST_START_GAME', { fillBots: true });
      return;
    }
  }

  // ---- 对局 ----

  onGameStart(d) {
    const mine = (d.cards || []).find((g) => g.id === this.posId);
    if (mine) {
      this.hand = mine.cards.slice();
      this.sortHand();
    }
    this.phase = 'playing';
    this.hostStarted = false;
    this.standingShape = null;
    this.standingPos = null;
    this.tmpFeng = 0;
    this.sumFeng = {};
    // 盘面记忆：以整副牌库存与各家初始张数重建，estimate 模式不看对手牌值。
    this.brain.startGame(d.cards || [], this.posId, (this.cfg.bot.brain && this.cfg.bot.brain.handInference) || 'estimate');
    this.initPlan();
    this.log('对局开始，手牌', this.hand.length, '张：', shape.handSummary(this.hand));
  }

  onShowTopCard(d) {
    if (typeof d.dizhuPosId === 'number') this.dizhuPosId = d.dizhuPosId;
    this.log('先手座位：', this.dizhuPosId);
  }

  onCtxPlayChange(d) {
    this.tmpFeng = d.tmpFeng != null ? d.tmpFeng : 0;
    if (d.sumFeng) this.sumFeng = d.sumFeng;

    // 记账：CTX_PLAY_CHANGE 是唯一记账源（含自己，服务器广播给自己）；replay 帧不记账（避免重连双计）。
    if (!d.replay) {
      const cards = d.ctxData && Array.isArray(d.ctxData.cards) ? d.ctxData.cards : [];
      const who = d.ctxData && typeof d.ctxData.posId === 'number' ? d.ctxData.posId : -1;
      if (cards.length) this.brain.recordPlay(who, cards);
      else if (d.isPass) this.brain.recordPass(who);
    }

    // 重连/重同步的 replay 帧与正常广播帧结构一致，统一处理；
    // 过牌帧（isPass）桌面待压牌与出牌者保持不变。
    if (d.clear) {
      this.standingShape = null;
      this.standingPos = null;
    } else if (!d.isPass && d.ctxData && d.ctxData.type) {
      this.standingShape = shape.parseShape(d.ctxData.type, d.ctxData.key, d.ctxData.len);
      this.standingPos = d.ctxData.posId;
    }

    const turn = d.posId;
    this.dbg('轮转：轮到座', turn, '桌面', this.standingShape ? shape.shapeLabel(this.standingShape) : '无（自由首出）', 'tmpFeng', this.tmpFeng);

    if (this.phase !== 'playing') return;
    if (turn === this.posId) {
      // 自己回合：消费 plan（共识/指令/约定）出牌。
      if (!this.busy) this.handleTurn();
    } else {
      // 他人回合：喂给协商线（给队友建议/放烟雾弹），规划与出牌解耦。
      this.onOthersTurn(turn);
    }
  }

  async handleTurn() {
    this.busy = true;
    const top = this.standingShape;

    // 主动协同：桌面是对手的牌而我压不住时，提前征求队友能否压。
    if (top && this.standingPos != null && !this.isTeammateSeat(this.standingPos)) {
      const can = this.brain.minimalBeater(this.hand, top);
      if (!can) this.maybeProactiveAsk();
    }

    let decision = null;
    let cards = [];
    let chat = '';

    try {
      decision = await this.decide(top);
    } catch (e) {
      this.log('决策异常，改用兜底：', e.message);
    }

    // 决策期间对局可能已结束（逃跑/终局）或状态被重置，此时不再出牌。
    if (this.phase !== 'playing') { this.busy = false; return; }

    if (decision) {
      cards = decision.cards || [];
      chat = decision.chat || '';
      this.absorbAdvice(decision.adviceFor);
    } else {
      const fb = this.fallbackMove(top);
      cards = fb.cards || [];
    }

    if (chat && this.cfg.bot.chat && this.cfg.bot.chat.onOwnTurn) this.sendChat(chat);
    this.lastSent = cards.length ? cards.slice() : null;
    if (cards.length) {
      this.dbg('出牌：', cards.map(shape.cardLabel).join(' '));
    } else {
      this.dbg('过牌');
    }
    this.sock.emit('PLAY_CARD', cards);
    this.busy = false;
  }

  // 把 LLM 给出的 adviceFor 写入 plan，待对应队友回合说出口。
  absorbAdvice(adviceFor) {
    if (!adviceFor || typeof adviceFor !== 'object') return;
    for (const [key, txt] of Object.entries(adviceFor)) {
      const seat = Number(key);
      const t = String(txt || '').trim();
      if (!Number.isInteger(seat) || !t) continue;
      if (seat === this.posId) continue; // 给自己？忽略
      this.plan.teammateAdvice[seat] = t;
      this.plan.version++;
    }
  }

  async decide(top) {
    if (!this.cfg.llm.apiKey) return null;
    try {
      const res = await this.llmDecide(top);
      if (!res) return null;
      const chat = (res.chat && String(res.chat).trim()) || '';
      const adviceFor = (res.adviceFor && typeof res.adviceFor === 'object') ? res.adviceFor : null;
      if (res.action === 'pass') return { action: 'pass', cards: [], chat, adviceFor };
      const concrete = this.concretize(res.cards);
      if (!concrete.ok) {
        this.log('LLM 出牌不合法或手牌不足，改用兜底；LLM 返回：', JSON.stringify(res.cards));
        return null;
      }
      if (!shape.canBeat(concrete.cards, top)) {
        this.log('LLM 出的牌压不过桌面，改用兜底；候选：', concrete.cards.map(shape.cardLabel).join(' '));
        return null;
      }
      return { action: 'play', cards: concrete.cards, chat, adviceFor };
    } catch (e) {
      this.log('LLM 决策失败，改用兜底：', e.message);
      return null;
    }
  }

  llmDecide(top) {
    const messages = [
      { role: 'system', content: this.systemPrompt() },
      { role: 'user', content: this.stateBlock(top) },
    ];
    return LLM.chatJSON(this.cfg.llm, messages, { temperature: this.cfg.llm.temperature });
  }

  // 把 LLM 返回的 {value} 列表落成手牌里的具体牌（同点牌任意花色互替，只信任 value）。
  concretize(cards) {
    if (!Array.isArray(cards) || !cards.length || cards.length > 24) return { ok: false };
    const vals = cards.map((c) => Number(c && c.value));
    if (vals.some((v) => !Number.isInteger(v) || v < 3 || v > 17)) return { ok: false };
    const v0 = vals[0];
    if (vals.some((v) => v !== v0)) return { ok: false };
    const avail = this.hand.filter((c) => c.value === v0);
    if (avail.length < vals.length) return { ok: false };
    return { ok: true, cards: avail.slice(0, vals.length) };
  }

  isTeammateSeat(posId) {
    return typeof posId === 'number' && posId >= 0 && (posId % 2) === (this.posId % 2);
  }

  // 是否应跳过压队友的牌（兜底策略，与服务器机器人“队友牌不吃/小牌可吃”同源）。
  shouldSkipBeating(top) {
    if (!top || !this.isTeammateSeat(this.standingPos)) return false;
    const dPass = this.plan.directives.slice(-6).find((x) => x.kind === 'pass' && x.applied);
    if (dPass) return true;
    const small = top.kind === 'single' || top.kind === 'pair' || top.kind === 'triple';
    return !(small && top.rank <= 8);
  }

  fallbackMove(top) {
    if (this.shouldSkipBeating(top)) return { cards: [] };
    const cards = shape.findHintCards(this.hand, top);
    return cards && cards.length ? { cards } : { cards: [] };
  }

  activeDirective(kind) {
    const arr = this.plan ? this.plan.directives : [];
    for (let i = arr.length - 1; i >= 0; i--) {
      if (arr[i].kind === kind && arr[i].applied) return arr[i];
    }
    return null;
  }

  // ---- 公开聊天 ----

  sendChat(text) {
    const msg = String(text || '').trim();
    if (!msg) return;
    this.log('发言：', msg);
    this.sock.emit('USER_MESSAGE', msg);
    this.chatLog.push({ posId: this.posId, name: this.cfg.bot.name, msg });
  }

  onMessage(d) {
    this.log('服务器提示：', d && d.msg);
    // 开局请求被拒（未准备/空位/非主持人等）：重置标记，满足条件后由状态变化再次触发。
    if (this.phase === 'lobby') this.hostStarted = false;
  }

  // ---- 协同协商线（问答 / 指令 / 建议，跨回合）----

  onUserMessage(d) {
    if (!d) return;
    const name = d.posId < 0 ? '观战' : this.seatName(d.posId);
    this.chatLog.push({ posId: d.posId, name, msg: d.msg });
    if (d.type === 'USER' || d.type === 'SYS') {
      this.dbg('聊天[' + (name || d.posId) + ']：', d.msg);
    }
    if (this.phase !== 'playing') return;
    if (d.posId === this.posId) return;        // 自己
    if (d.type === 'SYS') return;              // 系统消息
    if (d.posId < 0) return;                   // 观战者
    if (!this.isTeammateSeat(d.posId)) return; // 对手发言不可信、不消费

    this.handleTeammateChat(d);
  }

  handleTeammateChat(d) {
    const text = String(d.msg || '').trim();
    if (!text) return;
    const posId = d.posId;
    const fp = this.qhFingerprint(posId, text);

    // 任何同伴新发言都视为可能已响应进行中的群发问题（兜底据此闭嘴）。
    if (this.pendingGroup && this.pendingGroup.fp !== fp) this.pendingGroup.answered = true;

    // 1) 报牌/汇报：记录进 teammateClaims，无需他人作答。
    const report = this.parseHandReport(text);
    if (report.isReport) {
      this.teammateClaims[posId] = { text, values: report.values, ts: Date.now() };
      return;
    }

    const isQ = this.isQuestion(text);
    const toMe = this.mentionsMe(text);
    const toSeat = this.directedSeat(text);
    const directed = toMe ? this.posId : (toSeat >= 0 ? toSeat : -1);

    if (isQ) {
      if (directed === this.posId) {
        // 点名询问：必须回答（即便“没有/压不住”）。
        this.replyQuestion(text, posId, 'point', fp);
      } else if (directed === -1) {
        // 群发询问：有有用信息才答；否则由“上一个队友”兜底。
        this.replyQuestion(text, posId, 'open', fp);
      }
      // 点名别人：与我无关。
      return;
    }

    // 2) 指令/约定（非问句）。
    const assign = this.parseAssign(text);
    if (assign) {
      if (directed === this.posId) {
        this.applyDirective(posId, assign, text);
      } else if (directed === -1) {
        this.plan.commitments[fp] = { posId, text, kind: assign.kind };
        this.plan.version++;
        this.dbg('记录全队约定：', assign.kind, '：', text);
      }
      return;
    }

    // 3) 寒暄/建议/其它：不进指令，不强制动作（prompt 会引用聊天记录）。
  }

  qhFingerprint(askerPosId, text) {
    const norm = String(text).replace(/[\s，。！？!.,]/g, '');
    return askerPosId + '|' + norm.slice(0, 20);
  }

  mentionsMe(text) {
    if (text.includes(this.cfg.bot.name)) return true;
    return new RegExp('(^|[^0-9])' + this.posId + '\\s*[号座位]').test(text);
  }

  directedSeat(text) {
    for (let i = 0; i < 8; i++) {
      if (new RegExp('(^|[^0-9])' + i + '\\s*[号座位]').test(text)) return i;
    }
    for (let i = 0; i < 8; i++) {
      const nm = this.seats[i] && this.seats[i].userName;
      if (nm && nm !== this.cfg.bot.name && text.includes(nm)) return i;
    }
    return -1;
  }

  isQuestion(text) {
    return /[？?]/.test(text) ||
      /谁|有没有|有吗|有没|能不能|能否|可以吗|几张|多少|还剩|还有几个|能压|能接|能顶|压得住|接得住|谁有|可压|接这|顶这/.test(text);
  }

  parseHandReport(text) {
    if (/(我有|我留|我剩|我拿|我这边有|两条|两张|三个|有三|炸弹|王炸)/.test(text)) {
      const values = shape.parseValueMentions(text);
      return { isReport: true, values };
    }
    return { isReport: false };
  }

  parseAssign(text) {
    if (/别拆|不拆|不要拆|保留对|保留三/.test(text)) return { kind: 'dontBreak' };
    if (/留.{0,6}(王炸|炸弹|大王|小王)|别炸|别用炸弹|不要炸|别动炸/.test(text)) return { kind: 'keepBomb' };
    if (/你来|你接|你去压|你压|接这手|这手.{0,3}(你|接)/.test(text)) return { kind: 'take' };
    if (/别压|别接|你让|让.{0,3}过|别管|你先走|别抢/.test(text)) return { kind: 'pass' };
    return null;
  }

  directiveHarmful(assign) {
    // take：桌面若是队友的牌，抢队友的收分/接风通常有害 → 不跟。
    if (assign.kind === 'take') {
      return this.standingPos != null && this.isTeammateSeat(this.standingPos);
    }
    return false;
  }

  applyDirective(posId, assign, text) {
    const harmful = this.directiveHarmful(assign);
    this.plan.directives.push({ posId, kind: assign.kind, text, applied: !harmful, harmful });
    this.plan.version++;
    if (harmful) {
      this.dbg('拒绝损害己方指令：', text);
      this.replyChat('这手对咱们不利，先不跟');
      return;
    }
    this.dbg('接受指令：', assign.kind, '来自座位', posId);
    this.replyChat('收到');
  }

  qaEnabled() {
    const qa = this.cfg.bot.qa || {};
    return qa.enabled !== false;
  }

  canChatNow() {
    const qa = this.cfg.bot.qa || {};
    const now = Date.now();
    if (now - (this.lastChatAt || 0) < (qa.minIntervalMs || 0)) return false;
    this.qaReplyTimes = (this.qaReplyTimes || []).filter((t) => now - t < 60000);
    if (this.qaReplyTimes.length >= (qa.maxReplyPerMin || 1e9)) return false;
    return true;
  }

  markChatSent() {
    this.lastChatAt = Date.now();
    this.qaReplyTimes = this.qaReplyTimes || [];
    this.qaReplyTimes.push(Date.now());
  }

  // 协商发言：受 qa 节流约束（与回合发言 sendChat 区分）。
  replyChat(text) {
    const msg = String(text || '').trim();
    if (!msg) return;
    if (!this.qaEnabled()) return;
    if (!this.canChatNow()) return;
    this.markChatSent();
    this.sendChat(msg);
  }

  // 同队“上一个未出完队友”（顺时针向前数，跳过已出完者）。
  prevActiveTeammate(askerPosId) {
    let p = askerPosId;
    for (let k = 0; k < 4; k++) {
      p = (p - 2 + 8) % 8;
      if (p === askerPosId) continue;
      if (this.brain.remainCount[p] > 0) return p;
    }
    return -1;
  }

  replyQuestion(text, askerPosId, mode, fp) {
    if (this.plan.answerPending[fp]) return;
    const ans = this.buildAnswer(text, mode);
    if (mode === 'point') {
      // 点名必答。
      this.plan.answerPending[fp] = true;
      this.replyChat(ans.text || '知道了');
      return;
    }
    if (ans.hasInfo) {
      // 群发有有用信息才答。
      this.plan.answerPending[fp] = true;
      this.replyChat(ans.text);
      return;
    }
    // 无有用信息：仅“上一个队友”兜底，超时后必答。
    if (this.prevActiveTeammate(askerPosId) === this.posId) {
      this.scheduleGroupFallback(askerPosId, text, fp);
    }
  }

  scheduleGroupFallback(askerPosId, text, fp) {
    const qa = this.cfg.bot.qa || {};
    const ms = typeof qa.groupSilenceMs === 'number' ? qa.groupSilenceMs : 4000;
    this.pendingGroup = { fp, askerPosId, text, deadline: Date.now() + ms, answered: false };
    setTimeout(() => {
      if (this.phase !== 'playing') return;
      if (!this.pendingGroup || this.pendingGroup.fp !== fp) return;
      if (this.pendingGroup.answered) return;
      if (this.plan.answerPending[fp]) return;
      const ans = this.buildAnswer(text, 'fallback');
      this.plan.answerPending[fp] = true;
      this.replyChat(ans.text || '我这边暂时没有更多信息');
    }, ms);
  }

  buildAnswer(text, mode) {
    const values = shape.parseValueMentions(text);

    // 查张数。
    if (/几张|多少张|还剩几|还有几张/.test(text)) {
      return { text: '我剩' + this.hand.length + '张', hasInfo: true };
    }

    // 能否压/接当前桌面（须先于“有没有”判断：问句里的牌值往往是要压的目标，而非问我有没有）。
    if (/能压|能接|能顶|压得住|接得住|谁能|谁接|可压|接这|顶这/.test(text)) {
      const b = this.brain.minimalBeater(this.hand, this.standingShape);
      if (b) {
        const open = mode !== 'open' || this.disclosureOpen();
        return { text: '我能' + this.describeBeater(b, open), hasInfo: true };
      }
      return { text: '我压不住', hasInfo: false };
    }

    // 查牌/有无：能报则报（披露尺度受 coopDisclosure 约束）。
    if (values.length && (/(有|有没有|留|谁有|拿|剩余|报)/.test(text) || mode !== 'open')) {
      const myCounts = this.myHandValueCounts();
      const holding = values.filter((v) => (myCounts[v] || 0) > 0);
      if (holding.length) {
        const open = mode !== 'open' || this.disclosureOpen();
        return { text: this.haveReply(holding, myCounts, open), hasInfo: true };
      }
      return { text: '我没有' + values.map(shape.faceName).join(''), hasInfo: false };
    }

    return { text: '我这边没有更多信息', hasInfo: false };
  }

  myHandValueCounts() {
    const c = {};
    for (const card of this.hand) c[card.value] = (c[card.value] || 0) + 1;
    return c;
  }

  haveReply(holding, myCounts, open) {
    if (open) return '我有' + holding.map((v) => shape.faceName(v) + '×' + myCounts[v]).join('，');
    return '我有' + holding.map((v) => shape.faceName(v)).join('、');
  }

  describeBeater(b, open) {
    if (!open) return '压';
    if (b.kind === 'bomb' || b.kind === 'kingbomb') return shape.faceName(b.value) + '炸' + b.len + '张';
    return shape.kindName(b.kind) + shape.faceName(b.value);
  }

  disclosureOpen() {
    const br = this.cfg.bot.brain || {};
    if (br.coopDisclosure === 'open') return true;
    if (br.coopDisclosure === 'conservative') return false;
    const ow = br.openWhen || {};
    for (let i = 0; i < 8; i++) {
      if (i === this.posId || this.isTeammateSeat(i)) continue;
      const r = this.brain.remainCount[i];
      if (r != null && r > 0 && r <= (ow.anyOppRemainLte != null ? ow.anyOppRemainLte : 6)) return true;
    }
    if (this.teamCaptured(this.posId % 2) >= (ow.teamCapturedGte != null ? ow.teamCapturedGte : 250)) return true;
    if (ow.maxValueExhausted) {
      const c = this.myHandValueCounts();
      for (const v of [14, 15, 16, 17]) {
        if (this.brain.opponentMaxOf(v, c[v] || 0) === 0) return true;
      }
    }
    return false;
  }

  // 主动征求队友：桌面是对手的牌、我压不住时，问谁能压。
  maybeProactiveAsk() {
    if (!this.qaEnabled()) return;
    if (!this.standingShape) return;
    if (!this.canChatNow()) return;
    this.replyChat('这手我压不住，谁能压？');
  }

  // ---- 他人回合的协商：给队友建议 / 放烟雾弹 ----

  onOthersTurn(turn) {
    if (this.isTeammateSeat(turn)) this.maybeAdviseTeammate(turn);
    // 对手回合默认不发言（烟雾弹收益小且易泄露，保持静默）。
  }

  maybeAdviseTeammate(turn) {
    if (!this.cfg.bot.chat || !this.cfg.bot.chat.onOthersTurn) return;
    if (!this.qaEnabled()) return;
    if (this.phase !== 'playing') return;
    const now = Date.now();
    if (now - (this.lastCommentAt || 0) < (this.cfg.bot.chat.minIntervalMs || 5000)) return;
    this.lastCommentAt = now;

    // 优先说出已算好的、给该队友的建议。
    const pending = this.plan.teammateAdvice[turn];
    if (pending) {
      delete this.plan.teammateAdvice[turn];
      this.replyChat(pending);
      return;
    }

    if (!this.cfg.llm.apiKey) {
      const t = this.generateAdvice(turn);
      if (t) this.replyChat(t);
      return;
    }
    this.llmAdvise(turn).then((t) => { if (t) this.replyChat(t); }).catch(() => { /* 静默 */ });
  }

  generateAdvice(turn) {
    if (!this.standingShape) {
      return turn + '号，没大牌就先出小牌，我帮你断后';
    }
    if (!this.isTeammateSeat(this.standingPos)) {
      const b = this.brain.minimalBeater(this.hand, this.standingShape);
      if (b) return turn + '号，这手让我来压';
    }
    return '';
  }

  async llmAdvise(turn) {
    const sys =
      '你是《沟通牌》玩家 ' + this.cfg.bot.name + '。现在轮到你的队友 ' + turn + ' 号出牌。' +
      '请基于盘面，给队友一句简短有用的建议（或约定分工、询问关键信息），只在确实有帮助时发言，否则务必输出空字符串。' +
      '公开聊天全桌可见（对手也看得到），' + (this.disclosureOpen() ? '可以报牌值。' : '不要泄露具体牌值。') +
      '只输出一个 JSON 对象：{"chat":"一句话或空字符串"}';
    const user = this.stateBlock(this.standingShape) + '\n现在轮到队友 ' + turn + ' 号，请给建议。';
    const obj = await LLM.chatJSON(this.cfg.llm, [
      { role: 'system', content: sys },
      { role: 'user', content: user },
    ], { temperature: 0.6 });
    return (obj && typeof obj.chat === 'string' && obj.chat.trim()) || '';
  }

  // ---- prompt 组装 ----

  systemPrompt() {
    const team = this.posId % 2 === 0 ? '偶数队（座位 0/2/4/6）' : '奇数队（座位 1/3/5/7）';
    return [
      '你是（多人扑克）沟通牌游戏里的一名真人玩家，名字叫 ' + this.cfg.bot.name + '，坐在第 ' + this.posId + '号座位（0-7）。',
      '同奇偶座位是队友：偶数队 {0,2,4,6}，奇数队 {1,3,5,7}；你属于' + team + '。座位间隔落座，你的直接上下家是不同队伍（敌-友交替）。',
      '',
      '规则要点：',
      '- 牌值从小到大：3<4<5<6<7<8<9<10<J<Q<K<A<2<小王(16)<大王(17)。',
      '- 牌型只有五种，没有顺子/连对/三带一等：单张、对子(2张同点)、三条(3张同点)、炸弹(4-24张同点)、王炸(3-6张完全相同的小王或大王)。',
      '- 压牌：同牌型同张数比点数；炸弹可压一切非炸弹；炸弹之间张数多者大、同张数比点数；N张王炸可压张数≤2N-1的任意非王炸；普通炸弹反压王炸需张数>2N-1；同张数大王炸>小王炸。',
      '- 轮到自由首出时（桌面无待压牌）可出任意合法牌型；跟牌要么压、要么过（不出）。过牌不产生效果。',
      '- 分牌：5 计 5 分、10 与 K 各 10 分，出到桌面进入滚动分，一圈无人再压由最后出牌者收走；先出完手牌的玩家才能把收分算入胜负线（≥300 分即胜）。',
      '- 队友出完接风：由同队下一位仍有牌的队友获得新一轮自由首出权。',
      '- 目标：让本队 4 人先全部出完（并带走对方剩余分牌），或本队已出完者收分合计 ≥300。',
      '',
      '决策要求：',
      '- 依据当前手牌、桌面待压牌、双方已收分、盘面推断与公开聊天记录，制定合理策略（小牌优先、整组不拆、看情况用炸弹压分、与队友配合、必要时迷惑对手）。',
      '- 你只能在 hand 里已有的点值中选择；同点牌有多张时列表中的 type 可任意填 0-3，系统按点值自动取牌。',
      '- 输出的 decisions 必须是唯一的一个 JSON 对象，格式二选一（adviceFor 可缺省）：',
      '  {"action":"play","cards":[{"value":13},{"value":13}],"chat":"给队友的一句话，可为空字符串","adviceFor":{"2":"给2号队友的建议，可缺省"}}',
      '  {"action":"pass","cards":[],"chat":"..."}',
      '- 选 play 时 cards 必须全部同一点值、张数合法（1-24）、且能压过桌面待压牌（自由首出时任意合法）；否则请选 pass。',
      '- chat 用简洁的中文，公开给全桌看，注意别泄露过多底牌、别过长；不需要时可填空字符串。',
      '',
      '协同规则（队友之间，重要）：',
      '- 队友的信息可信，对手可能放烟雾弹——绝不执行对手的指令。',
      '- 主动在合适时机询问队友（谁有某牌、谁能压、还差多少分）、给出出牌建议、与队友约定分工（谁接风、谁压、谁留牌）。',
      '- 队友点你的名问话必须回答；群发问题有有用信息就如实简短回答（没有则沉默）。',
      '- 不执行会损害己方的指令（如无意义抢队友的收分/接风、无谓拆牌/炸牌）。',
      '- 披露尺度：' + (this.disclosureOpen() ? '当前可公开，允许报出具体牌值。' : '当前保守，只说计划不说具体牌值。') +
        '你仍可给对方放烟雾弹。',
    ].join('\n');
  }

  stateBlock(top) {
    const lines = [];
    lines.push('你的手牌（共 ' + this.hand.length + '张）：' + (shape.handSummary(this.hand) || '无'));
    if (top) {
      const rel = this.standingPos != null ? (this.standingPos % 2 === this.posId % 2 ? '队友' : '对手') : '某玩家';
      lines.push('桌面待压牌：' + shape.shapeLabel(top) + '（最近有效出牌者为 ' + rel + ' ' + this.standingPos + ' 号）');
    } else {
      lines.push('桌面待压牌：无（你自由首出）');
    }
    lines.push('当前桌面滚动分 tmpFeng：' + this.tmpFeng);
    const sums = [];
    for (let i = 0; i < 8; i++) sums.push(i + '号' + (Number(this.sumFeng[i]) || 0));
    lines.push('各座位已收分：' + sums.join('，'));
    lines.push('本队已收分合计：' + this.teamCaptured(this.posId % 2) + '，对方合计：' + this.teamCaptured(1 - this.posId % 2));

    const summary = this.brain.inferenceSummary(this.myHandValueCounts());
    if (summary) lines.push('盘面推断：\n' + summary);

    const dirs = this.plan.directives.filter((x) => x.applied).map((x) => this.planDirectiveText(x));
    if (dirs.length) lines.push('队友已下达并需你遵守的指令：' + dirs.slice(-4).join('；'));
    const myAdvice = this.plan.teammateAdvice[this.posId];
    if (myAdvice) lines.push('队友给你的建议（待执行）：' + myAdvice);
    const comm = Object.values(this.plan.commitments).slice(-6).map((c) => c.text);
    if (comm.length) lines.push('本队约定：' + comm.join('；'));
    const claims = this.teammateClaimsBlock();
    if (claims) lines.push('队友报牌：' + claims);

    lines.push('最近公开聊天：\n' + (this.chatBlock() || '（暂无）'));
    lines.push('请决策（只输出 JSON 对象）。');
    return lines.join('\n');
  }

  planDirectiveText(x) {
    const m = {
      take: '你来压/接这手',
      pass: '别压/让我过',
      dontBreak: '别拆对/拆三',
      keepBomb: '保留炸弹/王炸',
    };
    return m[x.kind] || x.text;
  }

  teammateClaimsBlock() {
    const arr = [];
    for (let i = 0; i < 8; i++) {
      if (this.teammateClaims[i]) arr.push(i + '号：' + this.teammateClaims[i].text);
    }
    return arr.join('；');
  }

  chatBlock() {
    if (!this.chatLog.length) return '';
    return this.chatLog.slice(-16).map((m) => (m.name || ('座位' + m.posId)) + '：' + m.msg).join('\n');
  }

  teamCaptured(parity) {
    let s = 0;
    for (let i = 0; i < 8; i++) if (i % 2 === parity) s += Number(this.sumFeng[i]) || 0;
    return s;
  }

  seatName(posId) {
    if (this.seats[posId] && this.seats[posId].userName) return this.seats[posId].userName;
    return '座位' + posId;
  }

  // ---- 出牌结果 ----

  onPlayCardSuccess() {
    if (this.lastSent && this.lastSent.length) {
      this.removeFromHand(this.lastSent);
      this.lastSent = null;
      this.sortHand();
    } else {
      this.lastSent = null;
    }
  }

  onPlayCardError() {
    // 被拒（多为越序）：不删牌，等服务端重推的轮转帧对齐轮次。
    this.log('出牌被拒（可能越序），已等待轮转帧重同步');
    this.lastSent = null;
  }

  removeFromHand(cards) {
    for (const c of cards) {
      const i = this.hand.findIndex((h) => h.value === c.value && h.type === c.type);
      if (i !== -1) this.hand.splice(i, 1);
    }
  }

  sortHand() {
    this.hand.sort((a, b) => a.value - b.value || a.type - b.type);
  }

  // ---- 终局 / 中断 ----

  onGameOver(d) {
    this.phase = 'over';
    this.busy = false;
    this.hostStarted = false;
    const myWin = Array.isArray(d.winner) && d.winner.indexOf(this.posId) !== -1;
    this.log('对局结束：', myWin ? '本队获胜' : '本队落败', '比分', d.score, '我方席位', (d.winner || []).join(','));
    this.hand = [];
    this.standingShape = null;
    this.standingPos = null;
    this.brain.reset();
    this.initPlan();

    if (this.cfg.bot.keepPlaying) {
      // 终局后座位自动回到未准备态（state=1），重新准备等待下一局。
      setTimeout(() => {
        if (this.stopping) return;
        this.log('重新准备，等待下一局……');
        this.phase = 'lobby';
        this.sock.emit('PREPARE');
      }, 2000);
    }
  }

  onDisconnected() {
    if (this.stopping) return;
    if (!this.cfg.bot.reconnect || !this.cfg.bot.reconnect.enabled) {
      this.log('连接断开，未启用自动重连，退出');
      process.exit(1);
      return;
    }
    // 游戏中掉线：服务器保留座位，重连后自动坐回；否则座位已被释放，需重新入座。
    this.expectReconnect = this.phase === 'playing';
    if (!this.expectReconnect) {
      this.deskId = -1;
      this.posId = -1;
      this.hostPosId = -1;
    }
    if (this.reconnectTimer) clearTimeout(this.reconnectTimer);
    const delay = Math.min(1000 * Math.pow(2, this.retry++), this.cfg.bot.reconnect.maxDelayMs || 10000);
    this.log('连接断开，', delay + 'ms 后重连……');
    this.reconnectTimer = setTimeout(() => {
      this.attachSocket();
      this.connect();
    }, delay);
  }
}

if (require.main === module) {
  const configs = loadConfigs();
  if (!configs.length) {
    console.error('[ai-bot] 没有可启动的 bot 配置');
    process.exit(1);
  }
  console.log('[ai-bot] 共启动 ' + configs.length + ' 个 AI bot：' + configs.map((c) => c.bot.name).join('、'));
  configs.forEach((cfg) => new AiBot(cfg).start());
}

module.exports = { AiBot, WsClient };