// e2e-bot.js multi-client E2E acceptance (login/full game/reconnect/escape/lobby/history)
// over plain WebSocket + JSON envelopes; requires `ws` from the repo-root node_modules.
// Usage: node e2e-bot.js [url]   (default http://127.0.0.1:8000)
// Scenario B needs a short --reconnect-timeout (e.g. 5s) on the server.
const WebSocket = require('ws');

// URL precedence: argv > E2E_URL > default. When required from other scripts,
// argv[2] is not ours — pass the URL via E2E_URL.
const BASE = process.argv[2] && !process.argv[2].startsWith('-')
  ? process.argv[2]
  : (process.env.E2E_URL || 'http://127.0.0.1:8000');
let failed = false;
function check(cond, label) {
  if (cond) { console.log('  PASS', label); }
  else { failed = true; console.error('  FAIL', label); }
}

class WsClient {
  constructor() {
    this.handlers = new Map();
    // Frames arriving with no listener yet are buffered here: back-to-back
    // frames in one tick can fire before the listener is attached.
    this.pending = new Map();
    this.queue = [];
    const u = new URL(BASE);
    u.protocol = u.protocol === 'https:' ? 'wss:' : 'ws:';
    u.pathname = '/ws';
    this.ws = new WebSocket(u.href, { perMessageDeflate: false });
    this.ws.on('error', () => {}); // stay silent; caller timeouts handle failures
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
    this.hostPos = -1; // host seat (first sitter, never transfers), kept via HOST_CHANGE
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
    // Permanent listener (not dynamic on/off) so it never consumes pending.
    this.socket.on('HOST_CHANGE', (d) => {
      if (d && typeof d.posId === 'number') this.hostPos = d.posId;
    });
  }
  emit(ev, data) { this.socket.emit(ev, data); }
  // wait() drains pending first (frames may arrive before the listener is
    // attached). Dynamic on/off handlers (tryPlay) must NOT consume pending —
    // their response always arrives after their own emit; consuming a stale
    // one would cause ghost completions.
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
    // First sitter gets hostPos via HOST_CHANGE; others read it from the reply.
    if (typeof d.hostPosId === 'number' && d.hostPosId !== -1) this.hostPos = d.hostPosId;
  }
  async prepare() {
    this.emit('PREPARE');
  }
  // Driver listeners live on the old socket after reconnect; caller re-attaches.
  async reconnect() {
    this.connect();
    this.gameOver = null;
    this.awaitReplayStart = true; // GAME_START right after RECONNECT is a hand replay
    this.emit('LOGIN', this.name);
    const rec = await this.wait('RECONNECT', 8000);
    check(rec.deskId === this.deskId && rec.posId === this.posId && rec.posInfos.length === 8,
      `${this.name} RECONNECT 坐回桌${rec.deskId}座${rec.posId}`);
    if (typeof rec.hostPosId === 'number') this.hostPos = rec.hostPosId;
    return rec;
  }
  takeCards(cards, replay = false) {
    const mine = cards.find((g) => g.id === this.posId);
    // Replay frames hold the current hand (may be short); only opening frames check 40/41.
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
  // Iron rule: no timeout here — the server always answers each PLAY_CARD
  // with SUCCESS or ERROR; a late frame misjudged as failure by a timeout
  // desyncs the client hand (once caused flaky scenario D failures). Real
  // deadlocks are caught by the game-level hardStop.
  tryPlay(cards) {
    return new Promise((resolve) => {
      const done = (v) => { cleanup(); resolve(v); };
      const onError = () => done(false); // invalid card → try the next one
      const onOk = () => done(true);
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

// Random table each run: leftover reserved seats (600s) can clog a fixed table.
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

// Host mode: the host (first sitter, non-transferable) sends HOST_START_GAME
// after everyone is ready. Always wait for all PREPARE_SUCCESS first — the
// server may otherwise see the start request before someone's PREPARE and
// reject it (once caused flaky A/D timeouts).
async function hostStart(bots) {
  const host = bots.find((b) => b.hostPos === b.posId);
  if (!host) throw new Error('未找到主持人（HOST_CHANGE 跟踪缺失）');
  host.emit('HOST_START_GAME');
}

// attachDriver mounts the full game driver; re-callable per bot after reconnect.
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
    // Back-to-back frames may dispatch in one tick; calling takeTurn directly
    // from the callback would re-enter. Track myTurn and allow one takeTurn in
    // flight, re-checking when it finishes.
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
    // The permanent driver drains frames that entered pending before attach
    // (reconnect replays can beat listener re-attachment). Dynamic on/off
    // (tryPlay) still never consumes pending.
    for (const [ev, fn] of [['GAME_START', onStart], ['CTX_PLAY_CHANGE', onCtx]]) {
      const buf = b.socket.pending.get(ev) || [];
      while (buf.length) fn(buf.shift());
    }
  };
  bots.forEach(attach);
  return { finished, failure: () => failure, attach };
}

// Scenario A: full game
async function scenarioFullGame() {
  console.log('场景 A：8 人完整牌局');
  const bots = await spawnBots('A');
  const budget = moveBudget(12000);
  const drv = attachDriver(bots, budget);

  for (const b of bots) b.emit('PREPARE');
  await Promise.all(bots.map((b) => b.wait('PREPARE_SUCCESS', 10000)));
  await hostStart(bots);
  const starter = await Promise.race(bots.map((b) => b.wait('GAME_START', 15000)));
  check(starter && Array.isArray(starter.cards) && starter.cards.length === 8, 'GAME_START 8 家手牌齐全');
  const total = starter.cards.reduce((s, g) => s + g.cards.length, 0);
  check(total === 324, `发牌总数 ${total} = 324`);

  const showTop = await Promise.race(bots.map((b) => b.wait('SHOW_TOP_CARD', 10000)));
  check(typeof showTop.dizhuPosId === 'number' && Array.isArray(showTop.topCards) && showTop.timeout === 45, 'SHOW_TOP_CARD');

  await drv.finished;
  if (drv.failure()) throw drv.failure();

  // GAME_OVER is a broadcast that arrives per-client at different times; wait for all.
  await Promise.all(bots.map((b) => (b.gameOver ? Promise.resolve() :
    Promise.race([new Promise((res) => b.socket.once('GAME_OVER', res)), sleep(5000)]))));

  const over = bots.find((b) => b.gameOver).gameOver;
  check(over.winner.length === 4 && over.loser.length === 4 && over.ratio >= 1, `GAME_OVER payload ${JSON.stringify(over)}`);
  check(bots.every((b) => b.gameOver), '所有客户端都收到 GAME_OVER');
  bots.forEach((b) => b.socket.close());
}

// Scenario B: disconnect mid-game → reserved → escape on timeout
async function scenarioEscape() {
  console.log('场景 B：游戏中掉线超时判逃跑');
  const bots = await spawnBots('B');
  bots.forEach((b) => b.emit('PREPARE'));
  await Promise.all(bots.map((b) => b.wait('PREPARE_SUCCESS', 10000)));
  await hostStart(bots);
  await Promise.race(bots.map((b) => b.wait('CTX_PLAY_CHANGE', 20000)));

  bots[3].socket.close(); // seat 3 disconnects
  const others = bots.filter((_, i) => i !== 3);

  // The game must stay reserved (not terminate) shortly after the drop;
    // the probe window must stay below the server reconnect-timeout.
  let early = false;
  const probes = others.map((b) => {
    const fn = () => { early = true; };
    b.socket.on('FORCE_EXIT_EV', fn);
    return { b, fn };
  });
  await sleep(2500);
  probes.forEach(({ b, fn }) => b.socket.off('FORCE_EXIT_EV', fn));
  check(!early, '掉线后对局保留（未立即终止）');

  // Termination path: with a short timeout B3 escaping triggers it; on a long
  // -timeout server, another player manually quits (UNSITDOWN, also an escape
  // termination) so the scenario always wraps up. The quitter has already left
  // and gets no FORCE_EXIT, so it's excluded from the waiters.
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

// Scenario C: lobby (login validation/dup names/seat takeover/quick join)
async function scenarioLobby() {
  console.log('场景 C：大厅');
  const a = new Bot('dup');
  await a.login();

  // Login validation: non-empty, ≤10 chars, not an online duplicate.
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

// Scenario D: disconnect and reconnect, game resumes
async function scenarioReconnect() {
  console.log('场景 D：断线重连续局');
  const bots = await spawnBots('D');
  const budget = moveBudget(20000);
  const drv = attachDriver(bots, budget);

  for (const b of bots) b.emit('PREPARE');
  await Promise.all(bots.map((b) => b.wait('PREPARE_SUCCESS', 10000)));
  await hostStart(bots);
  // Wait until ALL bots have hands (racing one would continue too early).
  await Promise.all(bots.map((b) => b.wait('GAME_START', 15000)));
  // Drop only after the first play (ensures the play phase is active).
  await Promise.race(bots.map((b) => b.wait('CTX_PLAY_CHANGE', 20000)));

  const victim = bots[5];
  if (!victim.hand.length) throw new Error('victim 未拿到手牌');
  // Wait until the victim is idle (not their turn, nothing in flight) to avoid
    // a play race at the moment of disconnection.
  for (let i = 0; i < 200 && (victim.myTurn || victim.busy); i++) await sleep(50);
  if (victim.myTurn || victim.busy) throw new Error('victim 未能进入空闲状态');
  const handSig = (h) => h.map((c) => c.value * 4 + c.type).sort().join(',');
  const before = handSig(victim.hand);
  victim.socket.close();
  await sleep(400); // let the server process the disconnect

  await victim.reconnect();
  // Iron rule: after RECONNECT always wait('GAME_START') explicitly — the two
  // frames may land in different event-loop ticks and a direct pending read
  // can miss the replay.
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

  if (mine) victim.takeCards(gs.cards, true); // align with the server hand (drops any in-flight play)
  // Re-attach the driver (drains replay frames that arrived before attach).
  drv.attach(victim);

  await drv.finished;
  if (drv.failure()) throw drv.failure();
  await Promise.all(bots.map((b) => (b.gameOver ? Promise.resolve() :
    Promise.race([new Promise((res) => b.socket.once('GAME_OVER', res)), sleep(5000)]))));
  check(bots.every((b) => b.gameOver), '重连对局打完，全员 GAME_OVER');
  bots.forEach((b) => b.socket.close());
}

// Scenario E: history
async function scenarioHistory() {
  console.log('场景 E：历史战绩');
  const a = new Bot('A0');
  await a.login(); // scenario A already played a game
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

// Scenario F: bot-fill mode (empty seats filled with server bots).
async function scenarioBotFill() {
  console.log('场景 F：人机模式（单人 + 7 机器人）');
  const human = new Bot('F0');
  await human.login();
  await human.sit(DESK, 3); // first sitter = host
  human.emit('PREPARE');
  await human.wait('PREPARE_SUCCESS', 10000);

  // Empty seats without fill confirmation: server refuses with a MESSAGE.
  human.emit('HOST_START_GAME');
  const deny = await human.wait('MESSAGE');
  check(deny && /空位/.test(deny.msg), `不确认填充被拒（${deny.msg}）`);

  // Confirmed fill: 7 bots sit down (POS_STATUS_CHANGE with isBot) and the game starts.
  const botSeats = [];
  human.socket.on('POS_STATUS_CHANGE', (d) => { if (d.isBot) botSeats.push(d); });
  const budget = moveBudget(12000);
  const drv = attachDriver([human], budget); // only the human seat needs driving
  human.emit('HOST_START_GAME', { fillBots: true });
  const starter = await human.wait('GAME_START', 15000);
  check(starter && starter.cards.length === 8, '人机局 GAME_START 8 家手牌齐全');
  await sleep(500);
  check(botSeats.length === 7 && botSeats.every((d) => d.state === 2 && d.userName),
    `7 个机器人入座（实际 ${botSeats.length}）`);

  await drv.finished;
  if (drv.failure()) throw drv.failure();
  check(human.gameOver && human.gameOver.winner.length === 4, '人机局正常 GAME_OVER');

  // Wait for the bot-seat cleanup frames (POS_STATUS_CHANGE state=0).
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
    // ws.close() sends the close frame asynchronously; wait a beat before
    // exiting so the server registers the disconnect and frees the seats.
    await sleep(150);
    process.exit(failed ? 1 : 0);
  } catch (e) {
    console.error('E2E ERROR:', e.message);
    process.exit(1);
  }
})();

// Exports for e2e-audit.js etc.; requiring does not run the scenarios.
module.exports = { WsClient, Bot, connect, check, sleep, moveBudget, spawnBots, attachDriver, waitEvent, BASE };
