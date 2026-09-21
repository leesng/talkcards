// ai-bot.js LLM 驱动的沟通牌真人玩家（普通 WebSocket 客户端接入）。
// 与真人完全同构：登录 / 入座 / 准备 / 出牌 / 过牌 / 公开聊天 / 断线重连。
// 决策由 LLM 给出，ai-shape 校验，非法或 LLM 失败时回退到确定性策略，绝不卡死。
// 不改动任何后端 Go / 前端 JS，不依赖服务端 fillBots。
'use strict';

const { loadConfigs } = require('./ai-config');
const LLM = require('./ai-LLM');
const shape = require('./ai-shape');

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
    this.log('对局开始，手牌', this.hand.length, '张：', shape.handSummary(this.hand));
  }

  onShowTopCard(d) {
    if (typeof d.dizhuPosId === 'number') this.dizhuPosId = d.dizhuPosId;
    this.log('先手座位：', this.dizhuPosId);
  }

  onCtxPlayChange(d) {
    this.tmpFeng = d.tmpFeng != null ? d.tmpFeng : 0;
    if (d.sumFeng) this.sumFeng = d.sumFeng;

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
      if (!this.busy) this.handleTurn();
    } else {
      this.maybeComment(turn);
    }
  }

  async handleTurn() {
    this.busy = true;
    const top = this.standingShape;
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

  async decide(top) {
    if (!this.cfg.llm.apiKey) return null;
    try {
      const res = await this.llmDecide(top);
      if (!res) return null;
      const chat = (res.chat && String(res.chat).trim()) || '';
      if (res.action === 'pass') return { action: 'pass', cards: [], chat };
      const concrete = this.concretize(res.cards);
      if (!concrete.ok) {
        this.log('LLM 出牌不合法或手牌不足，改用兜底；LLM 返回：', JSON.stringify(res.cards));
        return null;
      }
      if (!shape.canBeat(concrete.cards, top)) {
        this.log('LLM 出的牌压不过桌面，改用兜底；候选：', concrete.cards.map(shape.cardLabel).join(' '));
        return null;
      }
      return { action: 'play', cards: concrete.cards, chat };
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

  fallbackMove(top) {
    const cards = shape.findHintCards(this.hand, top);
    return cards && cards.length ? { cards } : { cards: [] };
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

  onUserMessage(d) {
    if (!d) return;
    const name = d.posId < 0 ? '观战' : this.seatName(d.posId);
    this.chatLog.push({ posId: d.posId, name, msg: d.msg });
    if (d.type === 'USER' || d.type === 'SYS') {
      this.dbg('聊天[' + (name || d.posId) + ']：', d.msg);
    }
  }

  seatName(posId) {
    if (this.seats[posId] && this.seats[posId].userName) return this.seats[posId].userName;
    return '座位' + posId;
  }

  maybeComment(turn) {
    if (!this.cfg.bot.chat || !this.cfg.bot.chat.onOthersTurn) return;
    if (!this.cfg.llm.apiKey) return;
    if (this.phase !== 'playing') return;
    const now = Date.now();
    if (now - (this.lastCommentAt || 0) < (this.cfg.bot.chat.minIntervalMs || 5000)) return;
    this.lastCommentAt = now;
    this.llmComment(turn)
      .then((txt) => { if (txt) this.sendChat(txt); })
      .catch(() => { /* 静默 */ });
  }

  async llmComment(turn) {
    const relation = (turn % 2 === this.posId % 2) ? '队友' : '对手';
    const sys =
      '你是《沟通牌》玩家 ' + this.cfg.bot.name + '，正在观战他人出牌。现在轮到' + relation + ' ' + turn + '号出牌。' +
      '你可以公开发一句简短的话（帮助队友制定策略、或放烟雾弹迷惑对手），但只在确实有帮助时发言，否则务必输出空字符串。' +
      '公开聊天全桌可见（对手也看得到），不要泄露自己的关键牌张。' +
      '只输出一个 JSON 对象：{"chat":"一句话或空字符串"}';
    const user =
      '你的手牌：' + (shape.handSummary(this.hand) || '无') + '\n' +
      '桌面待压牌：' + (this.standingShape ? shape.shapeLabel(this.standingShape) : '无（自由首出）') + '\n' +
      '当前桌面滚动分：' + this.tmpFeng + '\n' +
      '公开聊天：' + (this.chatBlock() || '（暂无）');
    const obj = await LLM.chatJSON(this.cfg.llm, [
      { role: 'system', content: sys },
      { role: 'user', content: user },
    ], { temperature: 0.8 });
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
      '- 依据当前手牌、桌面待压牌、双方已收分与公开聊天记录，制定合理策略（小牌优先、整组不拆、看情况用炸弹压分、与队友配合、必要时迷惑对手）。',
      '- 你只能在 hand 里已有的点值中选择；同点牌有多张时列表中的 type 可任意填 0-3，系统按点值自动取牌。',
      '- 输出的 decisions 必须是唯一的一个 JSON 对象，格式二选一：',
      '  {"action":"play","cards":[{"value":13},{"value":13}],"chat":"给队友的一句话，可为空字符串"}',
      '  {"action":"pass","cards":[],"chat":"..."}',
      '- 选 play 时 cards 必须全部同一点值、张数合法（1-24）、且能压过桌面待压牌（自由首出时任意合法）；否则请选 pass。',
      '- chat 用简洁的中文，公开给全桌看，注意别泄露过多底牌、别过长；不需要时可填空字符串。',
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
    lines.push('最近公开聊天：\n' + (this.chatBlock() || '（暂无）'));
    lines.push('请决策（只输出 JSON 对象）。');
    return lines.join('\n');
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

const configs = loadConfigs();
if (!configs.length) {
  console.error('[ai-bot] 没有可启动的 bot 配置');
  process.exit(1);
}
console.log('[ai-bot] 共启动 ' + configs.length + ' 个 AI bot：' + configs.map((c) => c.bot.name).join('、'));
configs.forEach((cfg) => new AiBot(cfg).start());