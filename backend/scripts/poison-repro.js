const { Bot, sleep } = require('./e2e-bot.js');


const fs = require('fs');
const path = require('path');
eval(fs.readFileSync(path.join(__dirname, '../web/static/js/parser.js'), 'utf8'));

const results = [];
function report(ok, label) {
  results.push({ ok, label });
  console.log(`  ${ok ? 'PASS' : 'FAIL'}  ${label}`);
}

function attachTrackers(h) {
  h.ctxCard = null; // {key,type,len,ctxPos}
  const trackCtx = (d) => {
    h.ctxCount++; h.lastCtx = d;
    if (!d.isPass && d.ctxData) {
      h.ctxCard = { key: d.ctxData.key, type: d.ctxData.type, len: d.ctxData.len, ctxPos: d.ctxData.posId };
    }
    if (d.clear) h.ctxCard = null;
  };
  h.socket.on('CTX_PLAY_CHANGE', trackCtx);
  h.socket.on('GAME_OVER', () => { h.gameOver = true; });
  h.socket.on('PLAY_CARD_ERROR', (d) => { h.errors.push(typeof d === 'string' ? d : 'array'); });
  // The first CTX frame may land in the pending buffer (same tick as
  // GAME_START): replay it into the tracker or lastCtx misses the lead frame.
  const pend = h.socket.pending.get('CTX_PLAY_CHANGE');
  while (pend && pend.length) trackCtx(pend.shift());
  return h;
}

// freshGame: own desk + bot mode (1 human + 7 server bots); returns a tracked Bot.
async function freshGame(name, desk) {
  const h = new Bot(name);
  await h.login();
  await h.sit(desk, 0); // first sitter = host
  h.emit('PREPARE');
  await h.wait('PREPARE_SUCCESS', 10000);

  h.hand = null;
  h.ctxCount = 0;
  h.lastCtx = null;
  h.gameOver = null;
  h.errors = []; // payloads: the string "游戏出错" or 'array'

  h.emit('HOST_START_GAME', { fillBots: true });
  const start = await h.wait('GAME_START', 15000);
  h.hand = start.cards.find((g) => g.id === h.posId).cards.slice();
  return attachTrackers(h);
}

// waitError: poll h.errors until the wanted payload shows up.
async function waitError(h, want, timeout = 8000) {
  const t0 = Date.now();
  while (Date.now() - t0 < timeout) {
    if (h.errors.includes(want)) return true;
    await sleep(100);
  }
  return false;
}

// waitCtxWhere: poll until lastCtx satisfies the predicate.
async function waitCtxWhere(h, pred, timeout = 90000) {
  const t0 = Date.now();
  while (Date.now() - t0 < timeout) {
    if (h.lastCtx && pred(h.lastCtx)) return h.lastCtx;
    await sleep(100);
  }
  return null;
}

// waitNewCtx: wait for a CTX frame arriving after the call (don't match stale ones).
async function waitNewCtx(h, timeout = 20000) {
  const base = h.ctxCount;
  const t0 = Date.now();
  while (Date.now() - t0 < timeout) {
    if (h.ctxCount > base) return h.lastCtx;
    await sleep(50);
  }
  return null;
}

// turnProbe: full post-rejection checks a+b+c+d. The exact resync frame shape
// is covered by the golden-frame assertions in internal/hub/turn_reject_test.go;
// here we assert the end-to-end observable behavior.
async function turnProbe(h, label) {
  const a = await waitError(h, 'array');
  report(a, `${label}: 触发帧收到数组错误（按普通违规拒绝）`);
  report(!h.errors.includes('游戏出错'), `${label}: 全程未出现 "游戏出错"（未毒化）`);
  const base = h.ctxCount;
  const alive = (await waitNewCtx(h, 8000)) !== null || h.gameOver;
  report(alive, `${label}: 触发后服务器仍向本座推帧（重同步/对局继续）`);
  if (h.gameOver) {
    console.log(`  >>> ${label}: 对局已自然终局，跳过出牌探针`);
    return;
  }
  // Playing normally on our turn still yields SUCCESS = phase still Playing
  // (before the fix this was rejected by the phase gate → bots stalled → deadlock).
  const mine = await waitCtxWhere(h, (d) => d.posId === h.posId, 60000);
  report(!!mine, `${label}: 对局继续轮转到本座`);
  let played = false;
  for (let i = 0; i < h.hand.length && !played; i++) {
    played = await tryPlayT(h, [h.hand[i]], 5000);
  }
  if (!played) played = await tryPlayT(h, [], 5000);
  report(played, `${label}: 本座正常出牌/过牌仍获 SUCCESS`);
}

// S1/S3 need a bot leader (empty table before the lead, trick.Pos=-1: any
// single card is legal). If our seat leads (1/8 chance), retry on another desk.
async function freshGameBotLeads(tag) {
  for (let attempt = 0; attempt < 4; attempt++) {
    const h = await freshGame(`${tag}${attempt}`, nextDesk());
    const first = await waitCtxWhere(h, () => true, 15000);
    if (!first) throw new Error(`${tag}: 未收到首出轮转帧`);
    if (first.posId !== h.posId) return { h, first };
    console.log(`  ${tag}: 首出恰好轮到本座，换桌重试`);
    h.socket.close();
    await sleep(300);
  }
  throw new Error(`${tag}: 连续 4 桌首出都是本座（运气过差）`);
}

// Global desk assignment: desks are fresh after server start; sequential ids never collide.
let deskSeq = 1;
function nextDesk() {
  const d = deskSeq++;
  if (deskSeq > 20) deskSeq = 1;
  return d;
}

// tryPlay with an outer timeout: fail fast instead of hanging when the server never replies.
function tryPlayT(h, cards, timeout = 10000) {
  return Promise.race([
    h.tryPlay(cards),
    sleep(timeout).then(() => false),
  ]);
}

async function scenarioOutOfTurnPlay() {
  console.log('\nS1 越序出牌（过期轮转状态：别人回合出手一张能压桌面的合法牌）');
  const h = await freshGame('s1', nextDesk());
  console.log(`  桌${h.deskId} 座${h.posId}：自己回合正常跟牌，别人回合寻找可压时机`);
  let triggered = false;
  for (let round = 0; round < 120 && !triggered; round++) {
    await waitNewCtx(h, 20000); // wait for the next (new) frame
    const d = h.lastCtx;
    if (!d) throw new Error('S1: 20s 无新 CTX 帧');
    if (d.posId !== h.posId) {
      // Others' turn: on an empty table any single is legal; otherwise pick a beating single
      let cands;
      if (!h.ctxCard) {
        cands = [h.hand[0]];
      } else {
        const top = parseShape(h.ctxCard.type, h.ctxCard.key, h.ctxCard.len);
        cands = h.hand.filter((c) => shapeBeats({ kind: 'single', rank: c.value, len: 1 }, top));
      }
      // Try several cards within the bot delay window; each rejection is side-effect free
      for (const c of cands.slice(0, 5)) {
        h.emit('PLAY_CARD', [c]);
        if (await waitError(h, 'array', 800)) { triggered = true; break; }
      }
      if (triggered) {
        console.log(`  命中：轮到座${d.posId}${h.ctxCard ? '，桌面 ' + h.ctxCard.type + '(' + h.ctxCard.key + ')' : '，空桌'}，越序出牌被拒`);
        break;
      }
    } else {
      // Our turn: follow normally to keep the game moving (it would idle on us)
      let played = false;
      for (let i = 0; i < h.hand.length && !played; i++) {
        played = await tryPlayT(h, [h.hand[i]], 5000);
      }
      if (!played) { await tryPlayT(h, [], 5000); await sleep(200); }
    }
  }
  if (!triggered) throw new Error('S1: 120 轮内未命中越序时机');
  await turnProbe(h, 'S1 越序出牌');
  h.socket.close();
}

async function scenarioDuplicateSecondPlay() {
  console.log('\nS2 出牌后立即再出另一张（模拟 autoPlay 竞态 / 二次出手）');
  const h = await freshGame('s2', nextDesk());
  console.log(`  桌${h.deskId} 座${h.posId} 像真实玩家一样跟牌，直到第一次成功出牌`);
  // On our turn try singles one by one (e2e takeTurn semantics), pass if none
  // beat; after collecting a trick we get a free lead. After any successful
  // play the second card is always a legal single.
  let first = null;
  for (let round = 0; round < 40 && !first; round++) {
    const mine = await waitCtxWhere(h, (d) => d.posId === h.posId, 20000);
    if (!mine) throw new Error('S2: 20s 内未轮到本座');
    for (let i = 0; i < h.hand.length; i++) {
      if (await tryPlayT(h, [h.hand[i]], 5000)) { first = h.hand[i]; break; }
    }
    if (!first) {
      await tryPlayT(h, [], 5000); // pass and wait for the next round
      await sleep(200);
    }
  }
  if (!first) throw new Error('S2: 40 轮内未能成功出牌');
  const second = h.hand.find((c) => c.value !== first.value || c.type !== first.type);
  if (!second) throw new Error('S2: 手牌无法选出第二张不同的牌');
  console.log(`  已成功出 ${first.value}（SUCCESS），立即再出第二张 ${second.value}`);
  // Rotation has moved on; the second card is out-of-turn (before the fix: Accepted → poison)
  h.emit('PLAY_CARD', [second]);
  await turnProbe(h, 'S2 二次出手');
  h.socket.close();
}

async function scenarioOutOfTurnPass() {
  console.log('\nS3 越序过牌（PLAY_CARD[] 修复前曾跳过全部校验直达 apply）');
  const { h } = await freshGameBotLeads('s3');
  console.log(`  桌${h.deskId} 座${h.posId} 首出者座${h.lastCtx.posId}，发送空手过牌`);
  h.emit('PLAY_CARD', []);
  await turnProbe(h, 'S3 越序过牌');
  h.socket.close();
}

// S4: mimic mySocket's offline queue — queued emits flush before re-login on
// open; the stale PLAY_CARD should be dropped on the anonymous connection.
// Needs a second online human (trustee), or the game ends before reconnect.
async function scenarioStaleQueueReplay() {
  console.log('\nS4 断线期间过期出牌帧重放（预期：不毒化，帧被丢弃）');
  const desk = nextDesk();
  const h = new Bot('s4a');
  const w = new Bot('s4b');
  await h.login(); await h.sit(desk, 0);
  await w.login(); await w.sit(desk, 1);
  h.emit('PREPARE'); w.emit('PREPARE');
  await h.wait('PREPARE_SUCCESS', 10000);
  await w.wait('PREPARE_SUCCESS', 10000);

  h.hand = null; h.ctxCount = 0; h.lastCtx = null; h.gameOver = null; h.errors = [];
  h.emit('HOST_START_GAME', { fillBots: true });
  const start = await h.wait('GAME_START', 15000);
  h.hand = start.cards.find((g) => g.id === h.posId).cards.slice();
  attachTrackers(h);
  w.emit('TOGGLE_TRUSTEE'); // second human trustees out so the game keeps moving
  console.log(`  桌${h.deskId}：s4a(座0)=测试者，s4b(座1)=托管旁观`);

  const mine = await waitCtxWhere(h, (d) => d.posId === h.posId);
  if (!mine) throw new Error('S4: 未轮到本座');
  console.log(`  座0 轮到本座 → 断线（未发 close 帧），出牌帧滞留离线队列`);
  const stale = [h.hand[0]];
  h.socket.ws.terminate();
  await sleep(500);
  console.log('  等待断线超时自动托管代打（--reconnect-timeout 3）…');
  await sleep(6000);
  // Frontend-style reconnect: on the new socket PLAY_CARD queues first and
  // LOGIN after — flushed in that order on open, i.e. the stale play frame
  // reaches the server before the re-login.
  h.connect();
  h.awaitReplayStart = true;
  h.socket.emit('PLAY_CARD', stale); // socket not open yet → queued
  h.emit('LOGIN', h.name);
  const rec = await h.wait('RECONNECT', 8000);
  // Trackers were on the old socket; re-attach to the new one (replays count into ctxCount)
  h.ctxCount = 0; h.gameOver = null; h.errors = []; h.ctxCard = null;
  h.socket.on('CTX_PLAY_CHANGE', (d) => {
    h.ctxCount++; h.lastCtx = d;
    if (!d.isPass && d.ctxData) h.ctxCard = { key: d.ctxData.key, type: d.ctxData.type, len: d.ctxData.len, ctxPos: d.ctxData.posId };
    if (d.clear) h.ctxCard = null;
  });
  h.socket.on('GAME_OVER', () => { h.gameOver = true; });
  h.socket.on('PLAY_CARD_ERROR', (d) => { h.errors.push(typeof d === 'string' ? d : 'array'); });
  // GAME_START after reconnect is a hand replay: refresh from the server
  h.socket.on('GAME_START', (d) => {
    if (h.awaitReplayStart) {
      const mine = d.cards && d.cards.find ? d.cards.find((g) => g.id === h.posId) : null;
      if (mine) h.hand = mine.cards.slice();
      h.awaitReplayStart = false;
    }
  });
  console.log(`  已重连（桌${rec.deskId} 座${rec.posId}），重放帧后观察 12s（轮到本座时自驱跟牌）`);
  await sleep(1200); // let the replay frames (1-2 CTX) settle first
  const t0 = h.ctxCount;
  const tEnd = Date.now() + 12000;
  while (Date.now() < tEnd && !h.gameOver) {
    if (h.lastCtx && h.lastCtx.posId === h.posId) {
      // Our turn: the game would idle on the human — self-drive one play
      let played = false;
      for (let i = 0; i < h.hand.length && !played; i++) {
        played = await tryPlayT(h, [h.hand[i]], 3000);
      }
      if (!played) { await tryPlayT(h, [], 3000); }
    } else {
      await sleep(300);
    }
  }
  const alive = h.ctxCount > t0 || h.gameOver;
  const poisoned = h.errors.includes('游戏出错');
  report(!poisoned, 'S4: 过期帧未触发 "游戏出错"（被匿名丢弃）');
  report(alive, 'S4: 重连后对局仍在推进（CTX 持续 / 已终局）');
  h.socket.close();
  w.socket.close();
}

if (require.main === module) (async () => {
  try {
    // Each scenario gets its own desk
    await scenarioOutOfTurnPass();
    await scenarioDuplicateSecondPlay();
    await scenarioOutOfTurnPlay();
    if (process.env.POISON_RECONNECT === '1') {
      await scenarioStaleQueueReplay();
    } else {
      console.log('\nS4 跳过（需 POISON_RECONNECT=1 且服务器 --reconnect-timeout 3）');
    }
    const bad = results.filter((r) => !r.ok).length;
    console.log(`\n结果：${results.length - bad}/${results.length} 项符合预期`);
    console.log('注：S1-S3 每项 PASS = 越序被拒绝且对局继续（毒化已修复）。');
    await sleep(200);
    process.exit(bad === 0 ? 0 : 1);
  } catch (e) {
    console.error('POISON-REPRO ERROR:', e.message);
    process.exit(1);
  }
})();

module.exports = { freshGame, turnProbe, waitCtxWhere };
