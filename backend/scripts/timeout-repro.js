// timeout-repro.js verifies that a timeout auto-play hands the turn over
// normally: replicates index.html autoPlayCards (lead → hand[0], else pass)
// and asserts the server accepts it (SUCCESS, not ERROR) and the game then
// reaches GAME_OVER.
// TRY_WAIT_MS: per-card wait cap in ms (default 3000; 200 is enough locally
//   and never misjudges a failure as success). SKIP_TIMEOUT=1 skips the 46s
//   wait and only checks a full normal game.
// Usage: node timeout-repro.js <url>
const { WsClient, waitEvent, sleep } = require('./e2e-bot.js');

const BASE = process.argv[2] || 'http://127.0.0.1:8000';
const MY = 0;
const TRY_WAIT_MS = parseInt(process.env.TRY_WAIT_MS || '3000', 10);
const SKIP_TIMEOUT = process.env.SKIP_TIMEOUT === '1';

(async () => {
  const s = new WsClient();
  await waitEvent(s, 'DESKS', 5000).catch(() => {});
  s.emit('LOGIN', '超时哥');
  await waitEvent(s, 'LOGIN_SUCCESS', 5000);
  s.emit('SITDOWN', { deskId: 18, posId: MY });
  await waitEvent(s, 'SITDOWN_SUCCESS', 5000);
  s.emit('PREPARE');
  await waitEvent(s, 'PREPARE_SUCCESS', 10000);
  s.emit('HOST_START_GAME', { fillBots: true });
  const gs = await waitEvent(s, 'GAME_START', 15000);
  let hand = gs.cards[MY].cards.slice();

  // Frames arrive async: poll for SUCCESS/ERROR after each emit.
  let respSeq = 0, lastResp = null;
  s.on('PLAY_CARD_SUCCESS', () => { lastResp = { seq: respSeq, ok: true }; });
  s.on('PLAY_CARD_ERROR', () => { lastResp = { seq: respSeq, ok: false }; });
  const tryPlay = async (cards) => {
    respSeq++; lastResp = null;
    s.emit('PLAY_CARD', cards);
    const dl = Date.now() + TRY_WAIT_MS;
    while (Date.now() < dl) {
      if (lastResp && lastResp.seq === respSeq) return lastResp.ok;
      await sleep(10);
    }
    return null; // no response
  };

  // Mirrors the frontend ctxCard semantics: only need to beat when the last
  // valid play wasn't mine.
  let ctxPosMe = true, needBeat = () => !ctxPosMe;
  let playTurn = false, timedOutOnce = false;
  s.on('CTX_PLAY_CHANGE', (d) => {
    playTurn = d.posId === MY;
    if (!d.isPass) ctxPosMe = d.ctxData.posId === MY;
    if (d.clear) ctxPosMe = true;
    if (!d.replay && d.ctxData.posId === MY && !d.isPass) {
      d.ctxData.cards.forEach((c) => {
        const i = hand.findIndex((h) => h.value === c.value && h.type === c.type);
        if (i >= 0) hand.splice(i, 1);
      });
    }
  });

  let over = false;
  waitEvent(s, 'GAME_OVER', 240000).then((d) => { over = true; console.log('GAME_OVER winner=', d.winner); });

  const t0 = Date.now();
  while (!over && Date.now() - t0 < 300000) {
    if (!playTurn) { await sleep(100); continue; }
    if (!timedOutOnce && !SKIP_TIMEOUT) {
      console.log('[wait ] 46s 模拟前端超时...');
      playTurn = false;
      await sleep(46000);
      const cards = !needBeat() && hand.length ? [hand[0]] : [];
      console.log('[emit ] autoPlayCards', cards.length ? 'hand[0]' : 'pass');
      const ok = await tryPlay(cards);
      if (ok !== true) { console.log('FAIL: 超时自动出牌被拒/无响应 ok=', ok); break; }
      console.log('PASS: 超时自动出牌被受理，出牌权已移交下一家');
      timedOutOnce = true;
      continue;
    }
    // Then play normally: try singles (capped by TRY_WAIT_MS), else pass.
    playTurn = false;
    let played = false;
    for (let i = hand.length - 1; i >= 0 && !played; i--) {
      if (await tryPlay([hand[i]]) === true) { hand.splice(i, 1); played = true; }
    }
    if (!played) await tryPlay([]);
    await sleep(200);
  }
  const done = over && (timedOutOnce || SKIP_TIMEOUT);
  console.log(over ? (done ? 'PASS: 牌局正常推进到终局' : 'FAIL: 未经超时即结束') : 'FAIL: 300s 未终局');
  s.close();
  await sleep(300);
  process.exit(done ? 0 : 1);
})().catch((e) => { console.error('FAIL:', e.message); process.exit(1); });
