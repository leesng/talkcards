// timeout-repro.js 验证"轮到我超时自动出牌后出牌权正常移交"：
// 完全复刻 index.html 的 autoPlayCards（领出→hand[0]；否则过牌），
// 断言：超时动作被服务器受理（PLAY_CARD_SUCCESS，非 ERROR），
// 且之后牌局正常推进到 GAME_OVER。
// 环境变量：
//   TRY_WAIT_MS   逐张试牌等待 SUCCESS/ERROR 的上限（毫秒，默认 3000）；
//                 测试模式可设 TRY_WAIT_MS=200 大幅加速（本机服务器响应 <50ms，
//                 200ms 足够，超时按无响应处理会保守判失败，不会误判成功）
//   SKIP_TIMEOUT  =1 时跳过 46s 超时等待（仅验证正常打完整局，不做超时路径断言）
// 用法：node timeout-repro.js <url>
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

  // 等响应：emit 后轮询 SUCCESS/ERROR（帧异步投递）
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
    return null; // 无响应
  };

  // 复刻前端 ctxCard 语义：最后一手有效牌不是"我"才需要压牌
  let ctxPosMe = true, needBeat = () => !ctxPosMe;
  let callTurn = false, playTurn = false, timedOutOnce = false;
  s.on('CTX_USER_CHANGE', (d) => { callTurn = d.ctxPos === MY; });
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
    if (callTurn) {
      s.emit('CALL_SCORE', { score: 3 });
      callTurn = false;
      await sleep(500);
      continue;
    }
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
    // 之后正常打：逐张试单张（等待上限 TRY_WAIT_MS），全被拒则过牌
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
