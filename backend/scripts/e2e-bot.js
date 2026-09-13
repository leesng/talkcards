// e2e-bot.js — 多客户端端到端验收：用户名登录 / 完整牌局 / 断线重连 / 逃跑超时 / 大厅 / 历史战绩。
// 通信层为纯 WebSocket + JSON 信封 {"type":..,"data":..}（需 ws 包于 NODE_PATH，
// 或使用仓库根 node_modules）。仅适用于 Go 版（原 Node 版为 socket.io 协议，不兼容）。
//   node e2e-bot.js [url]        默认 http://127.0.0.1:8000
// 注意：场景 B（逃跑超时）需服务端以较短 --reconnect-timeout（如 5 秒）启动。
const WebSocket = require('ws');

// URL 优先级：命令行参数 > E2E_URL 环境变量 > 默认。
// 注意 require 复用（node -e / 其它脚本）时 argv[2] 不属于本脚本，用 E2E_URL 传 URL。
const BASE = process.argv[2] && !process.argv[2].startsWith('-')
  ? process.argv[2]
  : (process.env.E2E_URL || 'http://127.0.0.1:8000');
let failed = false;
function check(cond, label) {
  if (cond) { console.log('  PASS', label); }
  else { failed = true; console.error('  FAIL', label); }
}

// ---------- 迷你客户端：on/once/off/emit/close，接口对齐旧 socket.io 用法 ----------
class WsClient {
  constructor() {
    this.handlers = new Map(); // type -> Set(fn)
    this.pending = new Map();  // type -> [data] 无监听器时暂存（同一 tick 背靠背多帧时，
                               // 新监听器要到微任务才挂上，不暂存会丢事件）
    this.queue = [];
    const u = new URL(BASE);
    u.protocol = u.protocol === 'https:' ? 'wss:' : 'ws:';
    u.pathname = '/ws';
    this.ws = new WebSocket(u.href, { perMessageDeflate: false });
    this.ws.on('error', () => {}); // 连接异常时静默，由上层等待超时兜底
    this.ws.on('open', () => { while (this.queue.length) this.ws.send(this.queue.shift()); });
    this.ws.on('message', (data) => {
      let m;
      try { m = JSON.parse(data.toString()); } catch (e) { return; }
      if (!m || !m.type) return;
      const set = this.handlers.get(m.type);
      if (set && set.size) { [...set].forEach((fn) => fn(m.data)); }
      else {
        const buf = this.pending.get(m.type) || [];
        if (buf.length < 100) buf.push(m.data);
        this.pending.set(m.type, buf);
      }
    });
    this.dead = new Promise((res) => this.ws.on('close', res));
  }
  on(ev, fn) {
    if (!this.handlers.has(ev)) this.handlers.set(ev, new Set());
    this.handlers.get(ev).add(fn);
    return this;
  }
  once(ev, fn) {
    const wrap = (d) => { this.off(ev, wrap); fn(d); };
    return this.on(ev, wrap);
  }
  off(ev, fn) { const s = this.handlers.get(ev); if (s) s.delete(fn); }
  emit(ev, data) {
    const msg = { type: ev };
    if (data !== undefined) msg.data = data;
    const s = JSON.stringify(msg);
    if (this.ws.readyState === 1) this.ws.send(s);
    else this.queue.push(s);
  }
  close() { this.ws.close(); }
}

function connect() { return new WsClient(); }

function waitEvent(socket, ev, timeout = 5000) {
  const buf = socket.pending.get(ev);
  if (buf && buf.length) return Promise.resolve(buf.shift());
  return new Promise((resolve, reject) => {
    const t = setTimeout(() => reject(new Error(`等待 ${ev} 超时`)), timeout);
    socket.once(ev, (d) => { clearTimeout(t); resolve(d); });
  });
}

class Bot {
  constructor(name) {
    this.name = name;
    this.posId = -1;
    this.deskId = -1;
    this.hostPos = -1; // 主持模式：当前主持人座位号（首坐者，权限不转移；常监听 HOST_CHANGE 维护）
    this.hand = [];
    this.gameOver = null;
    this.forceExit = null;
    this.socket = null;
    this.connect();
  }
  connect() {
    this.socket = connect();
    this.socket.on('GAME_OVER', (d) => { this.gameOver = d; });
    this.socket.on('FORCE_EXIT_EV', (d) => { this.forceExit = d; });
    // 常监听（非动态 on/off）：主持权授予/复位都据此更新，不吃 pending
    this.socket.on('HOST_CHANGE', (d) => {
      if (d && typeof d.posId === 'number') this.hostPos = d.posId;
    });
  }
  emit(ev, data) { this.socket.emit(ev, data); }
  // wait 先消费暂存事件（同一 tick 背靠背多帧时，监听器可能晚于帧注册），
  // 否则挂一次性监听。动态 on/off（tryPlay）不得消费暂存——
  // 其响应永远晚于自己的 emit，误消费旧响应会造成幽灵完成。
  wait(ev, timeout = 5000) {
    const buf = this.socket.pending.get(ev);
    if (buf && buf.length) {
      return Promise.resolve(buf.shift());
    }
    return new Promise((resolve, reject) => {
      const t = setTimeout(() => reject(new Error(`${this.name} 等待 ${ev} 超时`)), timeout);
      this.socket.once(ev, (d) => { clearTimeout(t); resolve(d); });
    });
  }
  async login() {
    this.emit('LOGIN', this.name);
    const desks = await this.wait('LOGIN_SUCCESS');
    check(Array.isArray(desks) && desks.length === 20 && desks[0].positions.length === 8,
      `${this.name} LOGIN_SUCCESS 桌型 20x8`);
    return desks;
  }
  async sit(deskId, posId) {
    this.emit('SITDOWN', { deskId, posId });
    const d = await this.wait('SITDOWN_SUCCESS');
    check(d.posId === posId && d.deskId === deskId && d.posInfos.length === 8,
      `${this.name} SITDOWN_SUCCESS`);
    this.posId = posId;
    this.deskId = deskId;
    // 首坐者会随后收到 HOST_CHANGE 常监听帧更新 hostPos；后坐者直接从回帧拿当前主持位
    if (typeof d.hostPosId === 'number' && d.hostPosId !== -1) this.hostPos = d.hostPosId;
  }
  async prepare() {
    this.emit('PREPARE');
    await this.wait('PREPARE_SUCCESS', 10000); // 等全桌准备后一起收到 GAME_START
  }
  // 重连后驱动监听挂在旧 socket 上，需由调用方重新 attach
  async reconnect() {
    this.connect();
    this.gameOver = null;
    this.awaitReplayStart = true; // RECONNECT 后紧跟的 GAME_START 是当前手牌重放帧
    this.emit('LOGIN', this.name);
    const rec = await this.wait('RECONNECT', 8000);
    check(rec.deskId === this.deskId && rec.posId === this.posId && rec.posInfos.length === 8,
      `${this.name} RECONNECT 坐回桌${rec.deskId}座${rec.posId}`);
    if (typeof rec.hostPosId === 'number') this.hostPos = rec.hostPosId;
    return rec;
  }
  takeCards(cards, replay = false) {
    const mine = cards.find((g) => g.id === this.posId);
    // 重连重放帧是当前手牌（可不足开局张数），仅开局帧校验 40/41
    check(mine && (replay ? mine.cards.length >= 1 : (mine.cards.length === 40 || mine.cards.length === 41)),
      `${this.name} 手牌 ${mine ? mine.cards.length : 0} 张`);
    this.hand = mine.cards.slice();
  }
  async takeTurn(moveBudget) {
    for (let i = 0; i < this.hand.length && moveBudget.can(); i++) {
      moveBudget.use();
      const card = this.hand[i];
      const ok = await this.tryPlay([card]);
      if (ok) { this.hand.splice(i, 1); return; }
    }
    if (!moveBudget.can()) throw new Error('步数预算耗尽');
    moveBudget.use();
    await this.tryPlay([]);
  }
  // 服务器对每次 PLAY_CARD 必回其一（SUCCESS/ERROR 单发，出牌另有广播），故不设超时——
  // 拥堵时帧晚到若被超时误判"失败"，客户端手牌会与服务器脱同步（场景 D 曾因此偶发 FAIL）；
  // 真死锁由对局级 hardStop 兜底。
  tryPlay(cards) {
    return new Promise((resolve) => {
      const done = (v) => { cleanup(); resolve(v); };
      const onError = () => done(false);                       // 牌不合规 → 试下一张
      const onOk = () => done(true);                            // 出牌成功
      const onCtx = (d) => { if (d.ctxData.posId === this.posId && !d.isPass) done(true); };
      const cleanup = () => {
        this.socket.off('PLAY_CARD_ERROR', onError);
        this.socket.off('PLAY_CARD_SUCCESS', onOk);
        this.socket.off('CTX_PLAY_CHANGE', onCtx);
      };
      this.socket.on('PLAY_CARD_ERROR', onError);
      this.socket.on('PLAY_CARD_SUCCESS', onOk);
      this.socket.on('CTX_PLAY_CHANGE', onCtx);
      this.emit('PLAY_CARD', cards);
    });
  }
}

function moveBudget(n) {
  let left = n;
  return { can: () => left > 0, use: () => { left--; } };
}

const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

// 每次运行随机选一张桌，避免上一轮死局/保留座位残留（600s）占死固定桌号
const DESK = 1 + Math.floor(Math.random() * 20);
async function spawnBots(prefix) {
  const bots = [];
  for (let i = 0; i < 8; i++) {
    const b = new Bot(`${prefix}${i}`);
    await b.login();
    await b.sit(DESK, i);
    bots.push(b);
  }
  return bots;
}

// 主持模式：全员 PREPARE 后服务器不再自动开局，由主持人（首坐者，权限不转移）发 HOST_START_GAME。
// 须先等全员 PREPARE_SUCCESS 再开牌：各 bot 的 PREPARE 与主持人的 HOST_START_GAME 跨连接并发，
// 服务器可能先处理到开牌请求（此时还有人未准备）而拒绝（曾致场景 A/D 偶发超时）。
async function hostStart(bots) {
  const host = bots.find((b) => b.hostPos === b.posId);
  if (!host) throw new Error('未找到主持人（HOST_CHANGE 跟踪缺失）');
  host.emit('HOST_START_GAME');
}

// attachDriver 挂载完整对局驱动（叫分 + 出牌 + 终局）。
// 返回 { finished, failure, settle }；重连后对同一 bot 可再次调用（监听挂在新 socket 上）。
function attachDriver(bots, budget) {
  let failure = null;
  let settle;
  const finished = new Promise((res) => { settle = res; });
  const hardStop = setTimeout(() => {
    failure = failure || new Error('牌局超时');
    for (const b of bots) console.error(`    [超时现场] ${b.name} posId=${b.posId} myTurn=${b.myTurn} busy=${b.busy} 手牌${b.hand.length}张 最后CTX帧:轮到座${b.lastCtx ? b.lastCtx.posId : '?'}`);
    settle();
  }, 180000);

  const attach = (b) => {
    b.myTurn = false;
    b.busy = false;
    const onStart = (d) => { b.takeCards(d.cards, b.awaitReplayStart); b.awaitReplayStart = false; };
    // 出牌驱动：纯 WS 下背靠背多帧可能在同一 tick 派发，直接在回调里 takeTurn
    // 会重入。改为记录"是否轮到我"，同一时间只允许一个 takeTurn 在飞，结束后复查。
    const playIfTurn = async () => {
      while (b.myTurn) {
        b.busy = true;
        try {
          await b.takeTurn(budget);
        } catch (e) {
          failure = failure || e;
          settle();
          return;
        } finally {
          b.busy = false;
        }
      }
    };
    const onCtx = (d) => {
      b.lastCtx = { posId: d.posId, isPass: d.isPass, actor: d.ctxData && d.ctxData.posId };
      b.myTurn = d.posId === b.posId;
      if (b.myTurn && !b.busy) playIfTurn();
    };
    const onOver = () => { clearTimeout(hardStop); settle(); };
    b.socket.on('GAME_START', onStart);
    b.socket.on('CTX_PLAY_CHANGE', onCtx);
    b.socket.on('GAME_OVER', onOver);
    // 常驻驱动补吃 attach 前入 pending 的帧（断线重连：服务器重放帧可能先于
    // 重挂监听到达）。动态 on/off（tryPlay）依然绝不消费 pending——不变。
    for (const [ev, fn] of [['GAME_START', onStart], ['CTX_PLAY_CHANGE', onCtx]]) {
      const buf = b.socket.pending.get(ev) || [];
      while (buf.length) fn(buf.shift());
    }
  };
  bots.forEach(attach);
  return { finished, failure: () => failure, attach };
}

// 场景 A：完整牌局
async function scenarioFullGame() {
  console.log('场景 A：8 人完整牌局');
  const bots = await spawnBots('A');
  const budget = moveBudget(12000);
  const drv = attachDriver(bots, budget);

  for (const b of bots) b.emit('PREPARE');
  await Promise.all(bots.map((b) => b.wait('PREPARE_SUCCESS', 10000)));
  await hostStart(bots); // 主持模式：全员准备好后由主持人开牌
  const starter = await Promise.race(bots.map((b) => b.wait('GAME_START', 15000)));
  check(starter && Array.isArray(starter.cards) && starter.cards.length === 8, 'GAME_START 8 家手牌齐全');
  const total = starter.cards.reduce((s, g) => s + g.cards.length, 0);
  check(total === 324, `发牌总数 ${total} = 324`);

  const showTop = await Promise.race(bots.map((b) => b.wait('SHOW_TOP_CARD', 10000)));
  check(typeof showTop.dizhuPosId === 'number' && Array.isArray(showTop.topCards) && showTop.timeout === 45, 'SHOW_TOP_CARD');

  await drv.finished;
  if (drv.failure()) throw drv.failure();

  // GAME_OVER 是广播，但各客户端到达有先后；等全员收齐再断言
  await Promise.all(bots.map((b) => (b.gameOver ? Promise.resolve() :
    Promise.race([new Promise((res) => b.socket.once('GAME_OVER', res)), sleep(5000)]))));

  const over = bots.find((b) => b.gameOver).gameOver;
  check(over.winner.length === 4 && over.loser.length === 4 && over.ratio >= 1, `GAME_OVER payload ${JSON.stringify(over)}`);
  check(bots.every((b) => b.gameOver), '所有客户端都收到 GAME_OVER');
  bots.forEach((b) => b.socket.close());
}

// 场景 B：游戏中掉线 → 保留 → 重连超时判逃跑
async function scenarioEscape() {
  console.log('场景 B：游戏中掉线超时判逃跑');
  const bots = await spawnBots('B');
  bots.forEach((b) => b.emit('PREPARE'));
  await Promise.all(bots.map((b) => b.wait('PREPARE_SUCCESS', 10000)));
  await hostStart(bots);
  await Promise.race(bots.map((b) => b.wait('CTX_PLAY_CHANGE', 20000)));

  bots[3].socket.close(); // 3 号掉线
  const others = bots.filter((_, i) => i !== 3);

  // 掉线后短期内对局应保留（不立即终止）；检测窗口须小于服务端 reconnect-timeout
  let early = false;
  const probes = others.map((b) => {
    const fn = () => { early = true; };
    b.socket.on('FORCE_EXIT_EV', fn);
    return { b, fn };
  });
  await sleep(2500);
  probes.forEach(({ b, fn }) => b.socket.off('FORCE_EXIT_EV', fn));
  check(!early, '掉线后对局保留（未立即终止）');

  // 终止路径：短超时服务器由 B3 超时判逃触发；长超时服务器等不到则由他人
  // 主动退出（UNSITDOWN，同样走逃跑终止），保证场景可收尾、座位可释放。
  // 注意主动退出者自身已出房、收不到 FORCE_EXIT，等待对象不含退出者。
  const quitter = others[0];
  const rest = others.slice(1);
  const exited = Promise.all(rest.map((b) => b.wait('FORCE_EXIT_EV', 15000)));
  const how = await Promise.race([exited.then(() => 'timeout'), sleep(6000).then(() => 'manual')]);
  if (how === 'manual') {
    quitter.emit('UNSITDOWN');
    await exited;
  }
  check(rest.every((b) => b.forceExit && (b.forceExit.posId === 3 || b.forceExit.posId === quitter.posId)),
    `对局终止 FORCE_EXIT_EV（${how} 路径，posId=${rest[0].forceExit ? rest[0].forceExit.posId : '?'}）`);
  others.forEach((b) => b.socket.close());
}

// 场景 C：大厅（用户名登录校验/重名/占座/快速加入）
async function scenarioLobby() {
  console.log('场景 C：大厅');
  const a = new Bot('dup');
  await a.login();

  // 登录校验：非空 / ≤10 字 / 不与在线重名
  const r = connect();
  r.emit('LOGIN', '');
  const ef = await waitEvent(r, 'LOGIN_FAIL');
  check(ef.msg === '用户名不能为空', '空用户名 LOGIN_FAIL');

  r.emit('LOGIN', 'x'.repeat(11));
  const lf = await waitEvent(r, 'LOGIN_FAIL');
  check(lf.msg === '名字不超过10个字', '超长用户名 LOGIN_FAIL');

  r.emit('LOGIN', 'dup');
  const dup = await waitEvent(r, 'LOGIN_FAIL');
  check(dup.msg === '该用户名已存在', '同名在线 LOGIN_FAIL');
  r.close();

  a.emit('SITDOWN', { deskId: 2, posId: 0 });
  await a.wait('SITDOWN_SUCCESS');
  const c = new Bot('occupier');
  await c.login();
  c.emit('SITDOWN', { deskId: 2, posId: 0 });
  const err = await c.wait('SITDOWN_ERROR');
  check(err.msg === '该位置已有人', '占座 SITDOWN_ERROR');
  await c.wait('REFRESH_LIST');
  check(true, '占座后收到 REFRESH_LIST');

  c.emit('QUICK_JOIN');
  const q = await c.wait('QUICK_JOIN');
  check(q.success === true && q.deskId > 0 && q.posId >= 0, `QUICK_JOIN → 桌${q.deskId} 座${q.posId}`);
  a.socket.close();
  c.socket.close();
}

// 场景 D：断线重连续局
async function scenarioReconnect() {
  console.log('场景 D：断线重连续局');
  const bots = await spawnBots('D');
  const budget = moveBudget(20000);
  const drv = attachDriver(bots, budget);

  for (const b of bots) b.emit('PREPARE');
  await Promise.all(bots.map((b) => b.wait('PREPARE_SUCCESS', 10000)));
  await hostStart(bots);
  // 等全员都拿到手牌（只 race 一个会在部分 bot 尚未处理 GAME_START 时就继续）
  await Promise.all(bots.map((b) => b.wait('GAME_START', 15000)));
  // 等首手出牌后再掉线（保证处于出牌阶段）
  await Promise.race(bots.map((b) => b.wait('CTX_PLAY_CHANGE', 20000)));

  const victim = bots[5];
  if (!victim.hand.length) throw new Error('victim 未拿到手牌');
  // 等 victim 空闲（不轮到、无在飞出牌）再断线，避免掉线瞬间的出牌竞态
  for (let i = 0; i < 200 && (victim.myTurn || victim.busy); i++) await sleep(50);
  if (victim.myTurn || victim.busy) throw new Error('victim 未能进入空闲状态');
  const handSig = (h) => h.map((c) => c.value * 4 + c.type).sort().join(',');
  const before = handSig(victim.hand);
  victim.socket.close();
  await sleep(400); // 等服务端处理断开

  await victim.reconnect();
  // RECONNECT 后服务器必发 GAME_START 重放帧；两帧可能分属不同事件循环批次，
  // 直接读 pending 会偶发扑空，须显式等待
  const gs = await victim.wait('GAME_START', 5000).catch(() => null);
  const mine = gs && gs.cards.find((g) => g.id === victim.posId);
  if (mine) {
    const k = (c) => c.value + '-' + c.type;
    const a = new Map(), b2 = new Map();
    for (const c of victim.hand) a.set(k(c), (a.get(k(c)) || 0) + 1);
    for (const c of mine.cards) b2.set(k(c), (b2.get(k(c)) || 0) + 1);
    const diff = [...new Set([...a.keys(), ...b2.keys()])].filter((x) => a.get(x) !== b2.get(x));
    if (diff.length) console.error(`    [手牌差异] 掉线前多: ${diff.filter((x) => (a.get(x) || 0) > (b2.get(x) || 0)).join(' ')} | 重放多: ${diff.filter((x) => (b2.get(x) || 0) > (a.get(x) || 0)).join(' ')}`);
  }
  check(mine && handSig(mine.cards) === before, `${victim.name} 重连重放手牌与掉线前一致`);

  if (mine) victim.takeCards(gs.cards, true); // 以服务器手牌为准对齐（掉线瞬间若有在飞出牌，消除脱同步）
  // 重连前驱动监听挂在旧 socket 上，需重新 attach（含补吃 attach 前到达的重放帧）
  drv.attach(victim);

  await drv.finished;
  if (drv.failure()) throw drv.failure();
  await Promise.all(bots.map((b) => (b.gameOver ? Promise.resolve() :
    Promise.race([new Promise((res) => b.socket.once('GAME_OVER', res)), sleep(5000)]))));
  check(bots.every((b) => b.gameOver), '重连对局打完，全员 GAME_OVER');
  bots.forEach((b) => b.socket.close());
}

// 场景 E：历史战绩
async function scenarioHistory() {
  console.log('场景 E：历史战绩');
  const a = new Bot('A0');
  await a.login(); // 场景 A 已打过一局
  a.emit('HISTORY_LIST', { page: 1 });
  const list = await a.wait('HISTORY_LIST');
  check(list.total >= 1 && Array.isArray(list.list) && list.list.length >= 1,
    `HISTORY_LIST 共 ${list.total} 条`);
  const item = list.list[0];
  check(item.gameId > 0 && (item.endReason === 'normal' || item.endReason === 'escape'),
    `最新战绩 #${item.gameId} endReason=${item.endReason}`);

  a.emit('HISTORY_DETAIL', { gameId: item.gameId });
  const detail = await a.wait('HISTORY_DETAIL');
  const me = detail.players.find((p) => p.userName === 'A0');
  check(me && typeof me.sumFeng === 'number' && typeof me.remaining === 'number',
    `HISTORY_DETAIL 含 A0（${detail.players.length} 人）`);
  check(Array.isArray(detail.winner) && detail.winner.length === 4, '正常局 winner 4 人');

  const b3 = new Bot('B3');
  await b3.login();
  b3.emit('HISTORY_LIST', { page: 1 });
  const blist = await b3.wait('HISTORY_LIST');
  check(blist.total >= 1 && blist.list[0].endReason === 'escape', `逃跑者视角最新记录为 escape（${blist.total} 条）`);
  b3.emit('HISTORY_DETAIL', { gameId: blist.list[0].gameId });
  const bdetail = await b3.wait('HISTORY_DETAIL');
  check(!bdetail.winner || bdetail.winner.length === 0, '逃跑局无 winner');

  a.emit('HISTORY_DETAIL', { gameId: blist.list[0].gameId });
  const deny = await a.wait('HISTORY_FAIL', 5000).catch(() => null);
  check(deny && deny.msg === '对局记录不存在', '不能查看未参与的对局');

  a.socket.close();
  b3.socket.close();
}

// 场景 F：人机模式（主持人开牌时空位填充机器人，单人练习一整局）
async function scenarioBotFill() {
  console.log('场景 F：人机模式（单人 + 7 机器人）');
  const human = new Bot('F0');
  await human.login();
  await human.sit(DESK, 3); // 首坐者即主持人
  human.emit('PREPARE');
  await human.wait('PREPARE_SUCCESS', 10000);

  // 有空位但不确认填充：服务器拒绝并提示，不开局
  human.emit('HOST_START_GAME');
  const deny = await human.wait('MESSAGE');
  check(deny && /空位/.test(deny.msg), `不确认填充被拒（${deny.msg}）`);

  // 确认填充：7 个机器人入座（POS_STATUS_CHANGE 带 isBot）后开局
  const botSeats = [];
  human.socket.on('POS_STATUS_CHANGE', (d) => { if (d.isBot) botSeats.push(d); });
  const budget = moveBudget(12000);
  const drv = attachDriver([human], budget); // 机器人由服务器驱动，只需驱动真人座位
  human.emit('HOST_START_GAME', { fillBots: true });
  const starter = await human.wait('GAME_START', 15000);
  check(starter && starter.cards.length === 8, '人机局 GAME_START 8 家手牌齐全');
  await sleep(500);
  check(botSeats.length === 7 && botSeats.every((d) => d.state === 2 && d.userName),
    `7 个机器人入座（实际 ${botSeats.length}）`);

  await drv.finished;
  if (drv.failure()) throw drv.failure();
  check(human.gameOver && human.gameOver.winner.length === 4, '人机局正常 GAME_OVER');

  // 终局机器人座位清理：等待收尾 POS_STATUS_CHANGE(state=0)
  await sleep(500);
  human.emit('HISTORY_LIST', { page: 1 });
  const hlist = await human.wait('HISTORY_LIST');
  check(hlist.total >= 1 && hlist.list[0].playerNum === 8, `人机局落库 8 名玩家（total=${hlist.total}）`);

  human.socket.close();
}

if (require.main === module) (async () => {
  try {
    await scenarioLobby();
    await scenarioFullGame();
    await scenarioEscape();
    await scenarioReconnect();
    await scenarioHistory();
    await scenarioBotFill();
    console.log(failed ? '\nE2E: 存在失败项' : '\nE2E: 全部通过');
    // ws.close() 的关闭帧为异步发送；稍等一拍再退出，确保服务器收到
    // 断开并清理座位，避免殃及下一轮验收
    await sleep(150);
    process.exit(failed ? 1 : 0);
  } catch (e) {
    console.error('E2E ERROR:', e.message);
    process.exit(1);
  }
})();

// 供 e2e-audit.js 等复用（require 时不执行上面的场景）
module.exports = { WsClient, Bot, connect, check, sleep, moveBudget, spawnBots, attachDriver, waitEvent, BASE };
