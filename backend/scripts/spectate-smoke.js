// 观战模式手工冒烟：真人+机器人开局，中途观战，验证脱敏帧与退出。
// 用法：E2E_URL=http://127.0.0.1:8793 node backend/scripts/spectate-smoke.js
const { Bot, attachDriver, moveBudget, sleep } = require('./e2e-bot.js');

async function main() {
  // host 开一桌人机局（挂上驱动让主持人也自动出牌，避免牌局停在真人回合）
  const host = new Bot('主持人');
  await host.login();
  await host.sit(1, 0);
  host.emit('PREPARE');
  await host.wait('PREPARE_SUCCESS');
  // 挂驱动要在开牌前：GAME_START 需要载入手牌，否则主持人无牌可打会停牌
  attachDriver([host], moveBudget(12000));
  host.emit('HOST_START_GAME', { fillBots: true });
  await host.wait('GAME_START');
  await host.wait('SHOW_TOP_CARD');
  await host.wait('CTX_PLAY_CHANGE');
  await sleep(1500); // 打几手

  // 观战者中途加入
  const sp = new Bot('观战者');
  await sp.login();
  sp.emit('SPECTATE', { deskId: 1 });
  const ok = await sp.wait('SPECTATE_SUCCESS');
  console.log('SPECTATE_SUCCESS deskId=', ok.deskId, 'hostPosId=', ok.hostPosId);

  const gs = await sp.wait('GAME_START');
  const redacted = gs.cards.every((g) => Array.isArray(g.cards) && g.cards.length === 0 && typeof g.count === 'number');
  console.log('GAME_START 脱敏(无牌面有张数):', redacted, 'counts=', gs.cards.map((g) => g.count).join(','));
  if (!redacted) process.exit(1);

  await sp.wait('SHOW_TOP_CARD');
  let ctx = await sp.wait('CTX_PLAY_CHANGE');
  const n1 = ctx.ctxData.posId;
  ctx = await sp.wait('CTX_PLAY_CHANGE');
  console.log('观战持续收到出牌帧 posId:', n1, '->', ctx.ctxData.posId, 'sumFeng keys:', Object.keys(ctx.sumFeng).length);

  // 观战者操作应被拒/忽略（不出 PLAY_CARD_SUCCESS 响应即被服务端忽略）
  sp.emit('PLAY_CARD', []);
  sp.emit('USER_MESSAGE', '你好');
  await sleep(500);

  // 对局中的在座玩家不能观战（服务器拒绝，无 SPECTATE_SUCCESS 响应）
  host.emit('SPECTATE', { deskId: 1 });
  await sleep(500);
  if (host.socket.pending.get('SPECTATE_SUCCESS') && host.socket.pending.get('SPECTATE_SUCCESS').length) {
    console.log('FAIL 对局中在座玩家不应能观战');
    process.exit(1);
  }
  console.log('对局中在座玩家观战被拒 OK');

  // 等终局
  const over = await sp.wait('GAME_OVER', 180000);
  console.log('观战收到 GAME_OVER winner:', over.winner, 'score:', over.score);

  sp.emit('UNSITDOWN');
  await sp.wait('UNSITDOWN_SUCCESS');
  console.log('退出观战 OK（直接回大厅，不能反向准备）');

  // 大厅桌状态已复位（deskState=0）
  const lobby = new Bot('路人');
  const desks = await lobby.login();
  const d1 = desks.find((d) => d.deskId === 1);
  console.log('终局后 desk1.state =', d1.state, d1.state === 0 ? 'OK' : 'FAIL');
  process.exit(d1.state === 0 ? 0 : 1);
}
main().catch((e) => { console.error('FAIL', e.message); process.exit(1); });
