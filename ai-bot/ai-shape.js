// ai-shape.js 牌型 / 压牌 / 候选 / 牌面文案。
// 规则与 backend/internal/card（shape.go / card.go）及前端 parser.js 保持一致，
// 改压牌规则时三处需同步。
'use strict';

const FACE_NAMES = {
  3: '3', 4: '4', 5: '5', 6: '6', 7: '7', 8: '8', 9: '9', 10: '10',
  11: 'J', 12: 'Q', 13: 'K', 14: 'A', 15: '2', 16: '小王', 17: '大王',
};

const SUIT_SYMBOLS = { 0: '♥', 1: '♦', 2: '♠', 3: '♣' };

// 点值名 -> 数值（在线问答焦点识别用，含中文名/缩写/大小写）。
const VALUE_BY_NAME = {
  '3': 3, '4': 4, '5': 5, '6': 6, '7': 7, '8': 8, '9': 9, '10': 10,
  'j': 11, '杰克': 11, '11': 11,
  'q': 12, '圈': 12, '12': 12,
  'k': 13, '凯': 13, '13': 13,
  'a': 14, '尖': 14, '14': 14,
  '2': 15, '两': 15, '15': 15,
  '小王': 16, '小joker': 16, '小丑': 16, '16': 16,
  '大王': 17, '大joker': 17, '大鬼': 17, '17': 17,
};

function faceName(value) {
  return FACE_NAMES[value] || String(value);
}

// 从一句话里提取提到的点值（去重、升序）。支持 3-10 / JQKA / 小王大王 / “两条Q”“有没有K”。
function parseValueMentions(text) {
  if (typeof text !== 'string' || !text) return [];
  const found = new Set();
  const s = text.toLowerCase();
  // 优先匹配多字名称（小王/大王）与“几条/几张/两条”后紧跟的单字名。
  for (const key of ['小王', '大王']) {
    if (s.includes(key)) found.add(VALUE_BY_NAME[key]);
  }
  // 中文量词 + 牌名：如 “两个Q”“三张8”“有条K”。
  const quan = /[零一二两三四五六七八九十\d]+[张条个]\s*([3-9]|10|j|q|k|a|2)/g;
  let m;
  while ((m = quan.exec(s)) !== null) {
    const v = VALUE_BY_NAME[m[1]];
    if (v != null) found.add(v);
  }
  // 单字牌名顺带匹配（J/Q/K/A/2/3-10），不带量词也认。
  const single = /(10|[3-9jqka2])/g;
  while ((m = single.exec(s)) !== null) {
    const v = VALUE_BY_NAME[m[1]];
    if (v != null) found.add(v);
  }
  return Array.from(found).sort((a, b) => a - b);
}

function cardLabel(card) {
  if (card.value === 16 || card.value === 17) return FACE_NAMES[card.value];
  return (SUIT_SYMBOLS[card.type] || '') + faceName(card.value);
}

// 由 CTX_PLAY_CHANGE 的 {type,key,len} 还原桌面底牌牌型；空 / 无效返回 null。
function parseShape(type, rank, len) {
  const n = len > 0 ? len : 0;
  if (!type || !n) return null;
  if (type === 'A') return { kind: 'single', rank, len: 1 };
  if (type === 'AA') return { kind: 'pair', rank, len: 2 };
  if (type === 'AAA') return { kind: 'triple', rank, len: 3 };
  if (type.indexOf('AAAA') === 0) return { kind: 'bomb', rank, len: n };
  if (type.indexOf('XKING') === 0) return { kind: 'kingbomb', rank: 16, len: n };
  if (type.indexOf('DKING') === 0) return { kind: 'kingbomb', rank: 17, len: n };
  return null;
}

// 候选 c 是否压过桌面 t（与 shape.go Shape.Beats 一致）。
function shapeBeats(c, t) {
  if (!c || !t) return false;
  const cKing = c.kind === 'kingbomb';
  const tKing = t.kind === 'kingbomb';
  if (cKing && tKing) return c.len > t.len || (c.len === t.len && c.rank > t.rank);
  if (cKing) return t.len <= 2 * c.len - 1;
  if (tKing) return c.kind === 'bomb' && c.rank !== 16 && c.rank !== 17 && c.len > 2 * t.len - 1;
  if (c.kind === 'bomb' && t.kind === 'bomb') {
    if (c.len === t.len) return c.rank > t.rank;
    return c.rank !== 16 && c.rank !== 17 && c.len > t.len;
  }
  if (c.kind === 'bomb') return c.rank !== 16 && c.rank !== 17;
  if (c.kind === t.kind && c.len === t.len) return c.rank > t.rank;
  return false;
}

// 一手同点牌的所有合法解读（与 shape.go Classify 一致：基础牌型在前、王炸在后）。
function shapesOfPlay(value, len) {
  const shapes = [];
  if (len === 1) shapes.push({ kind: 'single', rank: value, len: 1 });
  else if (len === 2) shapes.push({ kind: 'pair', rank: value, len: 2 });
  else if (len === 3) shapes.push({ kind: 'triple', rank: value, len: 3 });
  else shapes.push({ kind: 'bomb', rank: value, len });
  if ((value === 16 || value === 17) && len >= 3 && len <= 6) {
    shapes.push({ kind: 'kingbomb', rank: value, len });
  }
  return shapes;
}

// 手牌候选分组：孤张单 → 对 → 三 → 炸弹，组内点值升序；炸弹按张数再点值排序。
// 与 bot.Candidates / parser.js hintCandidates 同策略，整组取牌不拆对/三。
function hintCandidates(hand) {
  const groups = {};
  const order = [];
  hand.forEach((card) => {
    if (!groups[card.value]) { groups[card.value] = []; order.push(card.value); }
    groups[card.value].push(card);
  });
  order.sort((a, b) => a - b);
  const buckets = [[], [], [], []]; // single / pair / triple / bomb
  order.forEach((value) => {
    const cards = groups[value];
    let gi = cards.length - 1;
    if ((value === 16 || value === 17) && cards.length >= 3) gi = 3;
    if (gi > 3) gi = 3;
    buckets[gi].push(cards);
  });
  buckets[3].sort((a, b) => {
    if (a.length !== b.length) return a.length - b.length;
    return a[0].value - b[0].value;
  });
  return buckets[0].concat(buckets[1], buckets[2], buckets[3]);
}

// 找到最小能压过 topShape 的整组牌；topShape 为 null 表示自由首出（取最小整组）。
// 无牌可压返回 null（建议过牌）。
function findHintCards(hand, topShape) {
  if (!hand || !hand.length) return null;
  const cands = hintCandidates(hand);
  if (!topShape) return cands.length ? cands[0].slice() : null;
  for (let i = 0; i < cands.length; i++) {
    const cards = cands[i];
    const shapes = shapesOfPlay(cards[0].value, cards.length);
    for (let j = 0; j < shapes.length; j++) {
      if (shapeBeats(shapes[j], topShape)) return cards.slice();
    }
  }
  return null;
}

// 判断一手具体牌是否合法（全部同点且张数 1-24）。
function classify(cards) {
  if (!cards || !cards.length || cards.length > 24) return [];
  const v = cards[0].value;
  for (const c of cards) if (c.value !== v) return [];
  return shapesOfPlay(v, cards.length);
}

// 判断一手牌能否压过 topShape（自由首出时任意合法即可）。
function canBeat(cards, topShape) {
  if (!topShape) return classify(cards).length > 0;
  for (const s of classify(cards)) if (shapeBeats(s, topShape)) return true;
  return false;
}

// 手牌按点值归纳为紧凑摘要，如 "3×2，10×3，K×1，大王×2"。
function handSummary(hand) {
  const groups = {};
  const order = [];
  hand.forEach((c) => {
    if (!groups[c.value]) { groups[c.value] = 0; order.push(c.value); }
    groups[c.value]++;
  });
  order.sort((a, b) => a - b);
  return order.map((v) => faceName(v) + '×' + groups[v]).join('，');
}

function kindName(kind) {
  return { single: '单张', pair: '对子', triple: '三条', bomb: '炸弹', kingbomb: '王炸' }[kind] || kind;
}

// 牌型中文描述，供 prompt 使用。
function shapeLabel(shape) {
  if (!shape) return '无';
  return kindName(shape.kind) + ' ' + faceName(shape.rank) + '（' + shape.len + '张）';
}

module.exports = {
  FACE_NAMES,
  SUIT_SYMBOLS,
  VALUE_BY_NAME,
  faceName,
  parseValueMentions,
  cardLabel,
  parseShape,
  shapeBeats,
  shapesOfPlay,
  hintCandidates,
  findHintCards,
  classify,
  canBeat,
  handSummary,
  kindName,
  shapeLabel,
};