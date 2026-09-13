// e2e-audit.js — 完整对局审计：8 个机器人打满一整局，
// 出牌策略「有牌权：有单（孤张）出单，无单出对，无对出三，最后才是炸弹」：
// 候选按 单(孤张)→对→三→炸弹 分组、组内牌值升序依次尝试，首手成功即出，
// 全部被拒则过牌（不拆对/三去凑压牌）。炸弹候选按掷骰放行：桌上有分 90%、
// 无分 10%，未命中跳过；手里只剩炸弹时不掷骰直接出最小炸弹（最后手段，
// 也避免首出者过牌引发全桌空转一圈）。逐手输出审计日志，终局做守恒核对。
//   node e2e-audit.js [url]     默认 http://127.0.0.1:8000
//   AUDIT_VERBOSE=1             额外输出每次被拒的尝试与跳过的炸弹
const { Bot, sleep, moveBudget, BASE } = require('./e2e-bot.js');

const VERBOSE = !!process.env.AUDIT_VERBOSE;
let failed = false;
function check(cond, label) {
  if (cond) { console.log('  PASS', label); }
  else { failed = true; console.error('  FAIL', label); }
}

// ---------- 牌面命名 ----------
const VN = { 14: 'A', 15: '2', 16: '小王', 17: '大王' };
const vname = (v) => (VN[v] || String(v));
function pname(cards) {
  if (!cards.length) return '过';
  const v = cards[0].value;
  const n = cards.length;
  if ((v === 16 || v === 17) && n >= 3) return `${n}王炸${vname(v)}`;
  return `${['单', '对', '三', '四炸'][n - 1] || n + '连炸'}${vname(v)}`;
}

// ---------- 候选出牌：单（孤张）→对→三→炸弹 分组，组内牌值升序 ----------
// 每个牌值按现有张数整体归组（1=孤张单、2=对、3=三、≥4 或 3+同王=炸弹），
// 不拆组：跟牌时宁可过牌也不把对/三拆成单张去压。
function candidates(hand) {
  const byV = new Map();
  for (const c of hand) {
    if (!byV.has(c.value)) byV.set(c.value, []);
    byV.get(c.value).push(c);
  }
  const groups = [[], [], [], []]; // 单/对/三/炸弹
  for (const v of [...byV.keys()].sort((a, b) => a - b)) {
    const cs = byV.get(v);
    let gi = cs.length - 1;
    if ((v === 16 || v === 17) && cs.length >= 3) gi = 3; // 3+ 同王 = 王炸
    if (gi > 3) gi = 3;
    groups[gi].push(cs.slice());
  }
  return [...groups[0], ...groups[1], ...groups[2], ...groups[3]];
}

// ---------- 炸弹放行掷骰（发牌与掷骰均随机，失败排查靠台账与 AUDIT_FRAMES） ----------
const isBomb = (cs) => cs.length >= 4 || (cs[0].value >= 16 && cs.length >= 3);
const bombGo = (tableFeng) => Math.random() < (tableFeng > 0 ? 0.9 : 0.1);

// ---------- 审计台账：以 0 号客户端视角记录每一条 CTX_PLAY_CHANGE 广播 ----------
class Ledger {
  constructor() {
    this.seq = 0;        // 第几手（含过牌）
    this.trick = 0;      // 第几轮（首出换人即新一轮）
    this.sumFeng = {};   // 最近一次广播的各家抓分
    this.frames = [];    // 全量台账 {seq, trick, player, isPass, cards}
    this.estHands = null; // 由 GAME_START 重建的各家手牌（牌数），用于守恒核对
  }
  onFrame(d) {
    this.seq++;
    const player = d.ctxData.posId;
    // 新一轮 = 收牌后下一手（首出帧的 posId 是下一位，不能用 posId===player 判断）
    const cards = (d.ctxData.cards || []).slice();
    if (process.env.AUDIT_FRAMES) {
      console.log(`  RAW #${this.seq}`, JSON.stringify({ ctx: d.ctxData, posId: d.posId, isPass: d.isPass, tmpFeng: d.tmpFeng, sumFeng: d.sumFeng }));
    }
    let banked = null;
    for (const [p, s] of Object.entries(d.sumFeng)) {
      const prev = this.sumFeng[p] || 0;
      if (s > prev) banked = { pos: p, delta: s - prev, total: s };
    }
    this.sumFeng = Object.assign({}, d.sumFeng);
    if (banked) {
      this.trick++;
      console.log(`  [轮 ${this.trick} 结束] 座${banked.pos} 收牌 +${banked.delta} 分（累计 ${banked.total}）`);
    }
    this.frames.push({ seq: this.seq, trick: this.trick, player, isPass: d.isPass, cards });

    const desc = d.isPass ? '过' : `出 ${pname(cards)}`;
    console.log(`  #${String(this.seq).padStart(3)} 座${player} ${desc}  → 轮到座${d.posId}${d.isPass ? '' : ` 桌面分 ${d.tmpFeng}`}`);

    if (this.estHands) {
      if (!d.isPass) {
        this.estHands[player] -= cards.length;
        if (this.estHands[player] < 0) {
          failed = true;
          console.error(`  FAIL 台账守恒：座${player} 出牌数超过手牌（#${this.seq}）`);
        }
      }
    }
  }
}

// ---------- 重放校验器：独立复刻服务端轮转/压牌/收牌逻辑，逐帧断言 ----------
// 规则来源 internal/game/game.go（Next/Validate/nextPosID/nextGroupPosID）：
//  - 轮转跳过空手；出完牌由队友（对家顺延两位）接手首出
//  - 一圈压不住回到上一手出牌人 → 收牌入账
//  - 压牌：同型同长比 key；更长非王炸弹(≥4)压制；王炸(3-6王)压 ≤2N-1 张炸弹；
//    王炸对王炸张数多者大、同张数大王压小王；普通炸弹反压王炸需张数 >2N-1
class Replayer {
  constructor() {
    this.hands = null;      // 各家牌数（GAME_START 重建）
    this.last = null;       // {type,key,len,pos}
    this.ctxPos = -1;
    this.tmpFeng = 0;
    this.sumFeng = {};
    this.errors = [];
    this.KINGMAX = { KING3: 5, KING4: 7, KING5: 9, KING6: 11 };
  }
  init(handLens, firstPos) {
    this.hands = handLens.slice();
    this.ctxPos = firstPos;
    this.last = { type: '', key: 0, len: 0, pos: firstPos };
  }
  nextPos(p) { // 顺时针下一个还有牌的人
    for (let i = 1; i < 8; i++) {
      const n = (p + i) % 8;
      if (this.hands[n] > 0) return n;
    }
    return -1;
  }
  nextGroupPos(p) { // 队友接手：对家顺延两位
    for (let i = 0; i < 3; i++) {
      const n = (p + 2 + 2 * i) % 8;
      if (this.hands[n] > 0) return n;
    }
    return -1;
  }
  beats(t, k, ln) { // 能否压住 last（王炸对王炸比张数/大小王；王炸压 ≤2N-1 张；普通炸弹反压王炸需 >2N-1；同型同长比 key）
    const L = this.last;
    const kingCap = (ty) => this.KINGMAX[ty.replace(/^([DX])/, '')];
    const isKing = (ty) => /^([DX])KING[3-6]$/.test(ty);
    const kingN = (ty) => parseInt(ty.slice(-1), 10);
    const kingBig = (ty) => ty[0] === 'D';
    if (isKing(t) && isKing(L.type)) {
      return kingN(t) > kingN(L.type) ||
             (kingN(t) === kingN(L.type) && kingBig(t) && !kingBig(L.type));
    }
    if (isKing(t) && L.len <= kingCap(t)) return true;
    if (isKing(L.type)) return ln >= 4 && k !== 16 && k !== 17 && ln > kingCap(L.type);
    if (ln > L.len && ln >= 4 && k !== 16 && k !== 17) return true;
    if (t === L.type && ln === L.len && k > L.key) return true;
    return false;
  }
  onFrame(seq, d) {
    if (!this.hands) return;
    const p = d.ctxData.posId;
    const cards = d.ctxData.cards || [];
    const t = String(d.ctxData.type || '');
    const k = d.ctxData.key;
    const ln = d.ctxData.len || 0;
    if (p !== this.ctxPos) {
      this.errors.push(`#${seq} 座${p} 越序出牌（应为座${this.ctxPos}）`);
      return;
    }
    // 开局引导帧（地主亮牌，len=0 非过牌）：仅确认轮到，不推进轮转
    if (ln === 0 && !d.isPass) return;
    if (!d.isPass) {
      if (this.last && this.last.pos !== p && this.last.len > 0 && !this.beats(t, k, ln)) {
        this.errors.push(`#${seq} 座${p} ${t}key=${k}len=${ln} 压不过 last(${this.last.type}key=${this.last.key}len=${this.last.len}座${this.last.pos})`);
      }
      this.hands[p] -= cards.length;
      // 分牌：5 计 5 分，10/K 计 10 分
      for (const c of cards) {
        if (c.value === 5) this.tmpFeng += 5;
        else if (c.value === 10 || c.value === 13) this.tmpFeng += 10;
      }
    }
    // 推进
    this.ctxPos = this.nextPos(p);
    if (!d.isPass) this.last = { type: t, key: k, len: ln, pos: p };
    // 收牌：一圈回到上一手出牌人
    if (this.last.pos === this.ctxPos) {
      this.sumFeng[this.last.pos] = (this.sumFeng[this.last.pos] || 0) + this.tmpFeng;
      this.tmpFeng = 0;
    }
    // 出完牌 → 队友接手
    if (!d.isPass && this.hands[p] === 0) {
      this.ctxPos = this.nextGroupPos(this.last.pos);
      this.last = { type: '', key: 0, len: 0, pos: this.ctxPos };
    }
  }
}
async function auditTurn(b, budget) {
  const cands = candidates(b.hand);
  const bombs = cands.filter(isBomb);
  const plain = cands.filter((c) => !isBomb(c));
  // 手里只剩炸弹：不再掷骰，从最小的炸起（最后手段，保证牌局推进）
  const ordered = plain.length === 0 && bombs.length > 0
    ? bombs.map((cand) => ({ cand, force: true }))
    : cands.map((cand) => ({ cand, force: false }));
  for (const { cand, force } of ordered) {
    if (isBomb(cand) && !force && !bombGo(b.tableFeng)) {
      if (VERBOSE) console.log(`    跳过炸弹 ${b.name} ${pname(cand)}（桌面分 ${b.tableFeng}）`);
      continue;
    }
    if (!budget.can()) throw new Error('步数预算耗尽');
    budget.use();
    if (VERBOSE) console.log(`    尝试 ${b.name} ${pname(cand)}`);
    const ok = await b.tryPlay(cand);
    if (ok) {
      for (const c of cand) {
        const i = b.hand.findIndex((x) => x.value === c.value && x.type === c.type);
        b.hand.splice(i, 1);
      }
      return;
    }
  }
  if (!budget.can()) throw new Error('步数预算耗尽');
  budget.use();
  await b.tryPlay([]); // 全部压不过 → 过牌
}

async function main() {
  console.log(`审计对局：8 机器人 · 有单出单→无单出双→无双出三→最后炸弹（孤张/对/三不拆组）· 炸弹掷骰：有分 90%/无分 10% · ${BASE}`);
  const bots = [];
  for (let i = 0; i < 8; i++) {
    const b = new Bot(`X${i}`);
    await b.login();
    await b.sit(3, i); // 独立用 3 号桌，便于与 e2e-bot 并行调试
    bots.push(b);
  }
  const budget = moveBudget(20000);
  const ledger = new Ledger();
  const replayer = new Replayer();

  let settle, failure;
  const finished = new Promise((res) => { settle = res; });
  const hardStop = setTimeout(() => { failure = failure || new Error('牌局超时'); settle(); }, 180000);

  // 台账只挂一个客户端视角即可（广播内容一致）；重放校验逐帧断言。
  // 重放器在首个 CTX 帧到达时以 estHands + 首个轮到者（地主）自动初始化。
  bots[0].socket.on('CTX_PLAY_CHANGE', (d) => {
    if (!replayer.hands && ledger.estHands) replayer.init(ledger.estHands, d.posId);
    ledger.onFrame(d);
    replayer.onFrame(ledger.seq, d);
  });

  bots.forEach((b) => {
    b.socket.on('CTX_USER_CHANGE', (d) => {
      if (d.ctxPos === b.posId && d.calledScores === undefined) b.emit('CALL_SCORE', { score: 3 });
    });
    b.socket.on('GAME_START', (d) => {
      b.takeCards(d.cards);
      if (!ledger.estHands) {
        // GAME_START 广播 8 家手牌：重建各家牌数做守恒核对
        ledger.estHands = d.cards.map((g) => g.cards.length);
        check(d.cards.reduce((s, g) => s + g.cards.length, 0) === 324,
          `发牌总数 324（${ledger.estHands.join('/')}）`);
      }
    });
    b.myTurn = false;
    b.busy = false;
    const playIfTurn = async () => {
      while (b.myTurn) {
        b.busy = true;
        try {
          await auditTurn(b, budget);
        } catch (e) {
          failure = failure || e;
          settle();
          return;
        } finally {
          b.busy = false;
        }
      }
    };
    b.socket.on('CTX_PLAY_CHANGE', (d) => {
      b.myTurn = d.posId === b.posId;
      if (b.myTurn) b.tableFeng = d.tmpFeng || 0;
      if (b.myTurn && !b.busy) playIfTurn();
    });
    b.socket.on('GAME_OVER', () => settle());
  });

  for (const b of bots) b.emit('PREPARE');
  // 主持模式：先等全员 PREPARE_SUCCESS，再由主持人（首坐者，权限不转移）开牌，
  // 避免开牌请求先于别人的 PREPARE 到达服务器被拒
  await Promise.all(bots.map((b) => b.wait('PREPARE_SUCCESS', 10000)));
  const host = bots.find((b) => b.hostPos === b.posId);
  check(!!host, '找到主持人');
  if (host) host.emit('HOST_START_GAME');
  await Promise.race(bots.map((b) => b.wait('GAME_START', 15000)));
  await finished;
  clearTimeout(hardStop);
  if (failure) throw failure;

  // 等全员收到 GAME_OVER
  await Promise.all(bots.map((b) => (b.gameOver ? Promise.resolve() :
    Promise.race([new Promise((res) => b.socket.once('GAME_OVER', res)), sleep(5000)]))));

  // ---------- 终局核对 ----------
  console.log('\n=== 终局核对 ===');
  const over = bots.find((b) => b.gameOver).gameOver;
  check(bots.every((b) => b.gameOver), '所有客户端收到 GAME_OVER');
  check(Array.isArray(over.winner) && over.winner.length === 4 && over.loser.length === 4,
    `胜负队形 winner=${JSON.stringify(over.winner)} loser=${JSON.stringify(over.loser)} ratio=${over.ratio}`);

  const played = ledger.frames.filter((f) => !f.isPass).reduce((s, f) => s + f.cards.length, 0);
  const passes = ledger.frames.filter((f) => f.isPass).length;
  const inHand = ledger.estHands.reduce((s, n) => s + n, 0);
  check(played + inHand === 324, `牌数守恒：出牌 ${played} + 手中 ${inHand} = ${played + inHand}`);

  // 独立重放校验：无越序、无非法压牌、终局各家牌数与收分同服务端一致
  check(replayer.errors.length === 0,
    `重放校验 ${ledger.frames.length} 帧：零违规${replayer.errors.length ? '（' + replayer.errors.slice(0, 3).join('；') + '…）' : ''}`);
  if (replayer.hands) {
    const diffHand = replayer.hands.map((n, p) => (n === ledger.estHands[p] ? null : `座${p} 重放${n}/台账${ledger.estHands[p]}`)).filter(Boolean);
    check(diffHand.length === 0, `重放终局牌数与台账一致${diffHand.length ? '（' + diffHand.join('；') + '）' : ''}`);
    const diffFeng = Object.keys(ledger.sumFeng).map((p) => {
      const a = replayer.sumFeng[p] || 0;
      const b = ledger.sumFeng[p] || 0;
      return a === b ? null : `座${p} 重放${a}/服务端${b}`;
    }).filter(Boolean);
    check(diffFeng.length === 0, `重放收分与服务端一致${diffFeng.length ? '（' + diffFeng.join('；') + '）' : ''}`);
  }

  // 赢家队要么全部出完（estHands=0），要么靠 ≥300 分终局
  const winTeam = over.winner;
  const winEmpty = winTeam.every((p) => ledger.estHands[p] === 0);
  const winScore = winTeam.reduce((s, p) => s + (ledger.sumFeng[String(p)] || 0), 0);
  check(winEmpty || winScore >= 300,
    `终局条件：赢家队 ${winEmpty ? '全部出完' : `抓分 ${winScore} ≥ 300`}`);

  // 各家出牌手数统计 + 审计摘要
  console.log('\n=== 审计摘要 ===');
  const perPos = {};
  for (let p = 0; p < 8; p++) {
    const plays = ledger.frames.filter((f) => f.player === p && !f.isPass);
    const ps = ledger.frames.filter((f) => f.player === p && f.isPass);
    perPos[p] = { 出牌: plays.length, 过牌: ps.length, 剩余: ledger.estHands[p] };
  }
  console.log('  座位 | 出牌手数 | 过牌 | 剩余');
  for (let p = 0; p < 8; p++) {
    console.log(`   ${p}   |   ${perPos[p].出牌}    |  ${perPos[p].过牌}  |  ${perPos[p].剩余}`);
  }
  console.log(`  合计：${ledger.frames.length} 手（出牌 ${ledger.frames.length - passes} + 过牌 ${passes}），${ledger.trick} 轮`);

  bots.forEach((b) => b.socket.close());
  await sleep(150); // 等 close 帧送达，服务器清理座位
  if (failed) { console.error('\nAUDIT: 存在失败项'); process.exit(1); }
  console.log('\nAUDIT: 全部通过');
  process.exit(0);
}

main().catch((e) => { console.error('AUDIT ERROR:', e.message); process.exit(1); });
