// ai-brain.js 盘面记忆与推断（纯确定性，不依赖 LLM、不碰 socket）。
// 职责：记牌（谁出过哪些点值）、库存推断（哪些点值对手不可能再持有）、
// 残牌估算（各座剩多少张）、压牌能力评估（有谁能压/最小代价）。
// 记账源：CTX_PLAY_CHANGE.ctxData（含自己与他人，服务器广播给自己），
// PLAY_CARD_SUCCESS 只用于删自己手牌，绝不再记一次账（避免双计）。
// 规则与 backend/internal/card（card.go/shape.go）及 ai-shape.js 保持一致。
'use strict';

const shape = require('./ai-shape');

// 6 副牌同构库存：value 3..15 各 24 张（每副 4 花色），小王 16 6 张、大王 17 6 张。
const DECK = {};
for (let v = 3; v <= 15; v++) DECK[v] = 24;
DECK[16] = 6;
DECK[17] = 6;
const SEATS = 8;

function zeroValues() {
  const m = {};
  for (let v = 3; v <= 17; v++) m[v] = 0;
  return m;
}

class Brain {
  constructor() {
    this.mode = 'estimate'; // estimate | full
    this.myPosId = -1;
    this.reset();
  }

  reset() {
    this.history = [];        // {posId, cards, isPass, ts}
    this.seenByValue = zeroValues(); // 全桌已公开打出的点值张数
    this.baseline = {};       // posId -> 开局张数
    this.remainCount = {};    // posId -> 剩余张数
    this.playedCount = {};    // posId -> 已出张数
    this.opponentHands = null; // full 模式：posId -> {value:count}（不含自己）
  }

  // handGroups: GAME_START.cards [{id, cards, ht, count?}]（count 仅观战脱敏帧有）。
  startGame(handGroups, myPosId, mode) {
    this.reset();
    this.mode = mode === 'full' ? 'full' : 'estimate';
    this.myPosId = myPosId;
    const fullHands = {};
    for (const g of handGroups || []) {
      if (g == null) continue;
      const n = Array.isArray(g.cards) ? g.cards.length : (typeof g.count === 'number' ? g.count : 0);
      this.baseline[g.id] = n;
      this.remainCount[g.id] = n;
      this.playedCount[g.id] = 0;
      if (this.mode === 'full' && g.id !== myPosId && Array.isArray(g.cards)) {
        const cnt = zeroValues();
        for (const c of g.cards) cnt[c.value]++;
        fullHands[g.id] = cnt;
      }
    }
    if (this.mode === 'full') this.opponentHands = fullHands;
  }

  // 记录一手真实出牌（含自己与他人；CTX_PLAY_CHANGE 为唯一记账源，replay 帧不调用本函数）。
  recordPlay(posId, cards) {
    if (!Array.isArray(cards) || !cards.length) return;
    for (const c of cards) {
      if (c && c.value >= 3 && c.value <= 17) this.seenByValue[c.value]++;
    }
    if (typeof posId === 'number' && posId >= 0 && posId < SEATS) {
      this.playedCount[posId] = (this.playedCount[posId] || 0) + cards.length;
      if (this.baseline[posId] != null) {
        this.remainCount[posId] = Math.max(0, this.baseline[posId] - this.playedCount[posId]);
      }
      if (this.opponentHands && this.opponentHands[posId]) {
        for (const c of cards) {
          if (c && this.opponentHands[posId][c.value] > 0) this.opponentHands[posId][c.value]--;
        }
      }
    }
    this.history.push({ posId, cards: cards.slice(), isPass: false, ts: Date.now() });
  }

  recordPass(posId) {
    this.history.push({ posId, cards: [], isPass: true, ts: Date.now() });
  }

  remainOf(posId) { return this.remainCount[posId] != null ? this.remainCount[posId] : 0; }
  seenOf(value) { return this.seenByValue[value] || 0; }

  // 该点值全桌“未知去向”还剩多少（残留在对手手牌里）；已排除公开打出与自己手牌。
  unseenOf(value, myHandCountOfValue = 0) {
    const total = DECK[value] || 0;
    return Math.max(0, total - (this.seenByValue[value] || 0) - (myHandCountOfValue || 0));
  }

  // 对手（除我以外）在 value 上最多还可能持有多少张（estimate 看库存差，full 看精确残牌）。
  opponentMaxOf(value, myHandCountOfValue = 0) {
    if (this.mode === 'full' && this.opponentHands) {
      let s = 0;
      for (const id of Object.keys(this.opponentHands)) s += this.opponentHands[id][value] || 0;
      return s;
    }
    return this.unseenOf(value, myHandCountOfValue);
  }

  // 已耗尽（对手不可能再持有）的点值列表（按牌值降序，方便“大王/A/2 已见尽”表述）。
  exhaustedValues(myHandCountByValue) {
    const out = [];
    for (let v = 17; v >= 3; v--) {
      if ((DECK[v] || 0) > 0 && this.opponentMaxOf(v, (myHandCountByValue && myHandCountByValue[v]) || 0) === 0) out.push(v);
    }
    return out;
  }

  // 生成一段中文盘面推断摘要，供 prompt 使用。
  inferenceSummary(myHandCountByValue) {
    const parts = [];
    const remainLines = [];
    for (let i = 0; i < SEATS; i++) {
      if (this.remainCount[i] != null && i !== this.myPosId) remainLines.push(shape.seatLabel(i) + '剩' + this.remainCount[i] + '张');
    }
    if (remainLines.length) parts.push('他人剩余张数：' + remainLines.join('，'));

    const exhausted = this.exhaustedValues(myHandCountByValue);
    if (exhausted.length) parts.push('已见尽（他人不可能再持有）：' + exhausted.map(shape.faceName).join('、'));

    if (this.mode === 'full' && this.opponentHands) {
      const opp = [];
      for (let i = 0; i < SEATS; i++) {
        if (i === this.myPosId || !this.opponentHands[i]) continue;
        opp.push(shape.seatLabel(i) + '残牌=' + this.valueCountSummary(this.opponentHands[i]));
      }
      if (opp.length) parts.push('他人精确残牌（full 推断）：' + opp.join('；'));
    }
    return parts.join('\n');
  }

  valueCountSummary(counter) {
    const order = [];
    for (let v = 3; v <= 17; v++) if (counter[v]) order.push(shape.faceName(v) + '×' + counter[v]);
    return order.join('，') || '空';
  }

  // ---- 压牌能力评估（问答/选人用）----

  // 从 hand 里找出最小能压 standingShape 的整组牌；压不住返回 null。
  minimalBeater(hand, standingShape) {
    if (!hand || !hand.length || !standingShape) return null;
    const cands = shape.hintCandidates(hand);
    for (let i = 0; i < cands.length; i++) {
      const cards = cands[i];
      const shapes = shape.shapesOfPlay(cards[0].value, cards.length);
      for (let j = 0; j < shapes.length; j++) {
        if (shape.shapeBeats(shapes[j], standingShape)) {
          return { cards: cards.slice(), value: cards[0].value, len: cards.length, kind: shapes[j].kind };
        }
      }
    }
    return null;
  }

  // 压牌代价（越小越“便宜”）：普通牌优先于炸弹；再比张数、最后比牌值。
  beatCost(beater) {
    if (!beater) return null;
    const isBomb = beater.kind === 'bomb' || beater.kind === 'kingbomb';
    return [isBomb ? 1 : 0, beater.len, beater.value];
  }

  // 从多名候选者（seat -> 手牌）中，按代价升序排出能压者；供 full 模式精准选人 / 问答收敛。
  rankBeaters(standingShape, seatHands) {
    const out = [];
    for (const seat of Object.keys(seatHands || {})) {
      const b = this.minimalBeater(seatHands[seat], standingShape);
      if (b) out.push({ seat: Number(seat), beater: b, cost: this.beatCost(b) });
    }
    out.sort((a, b) => {
      for (let i = 0; i < 3; i++) {
        if (a.cost[i] !== b.cost[i]) return a.cost[i] - b.cost[i];
      }
      return a.seat - b.seat;
    });
    return out;
  }

  // full 模式：某人（含自己）当前精确残牌；否则返回 null。
  exactHandOf(posId) {
    if (this.mode === 'full' && posId === this.myPosId) return null; // 自己用 this.hand
    return (this.opponentHands && this.opponentHands[posId]) || null;
  }
}

module.exports = { Brain, DECK };