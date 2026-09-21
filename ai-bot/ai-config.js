// ai-config.js 配置加载：内置默认值 <- ai-config.json <- 环境变量覆盖。
// 支持多 bot：ai-config.json 可用顶层 "bots":[{...},...] 数组，每项覆盖一个 bot 的
// bot.* 配置（name/join/host/chat/...），server / llm 为所有 bot 共享。
// 无 bots 数组时退化为单 bot（沿用顶层 "bot" 配置，向后兼容）。
// 加载优先级：环境变量 > 配置文件 > 默认值。apiKey 永不打印到日志。
'use strict';

const fs = require('fs');
const path = require('path');

const DEFAULTS = {
  server: {
    url: 'ws://127.0.0.1:8000/ws',
  },
  llm: {
    baseUrl: 'https://api.openai.com/v1',
    apiKey: '',
    model: 'gpt-4o-mini',
    timeoutMs: 30000,
    temperature: 0.2,
    jsonMode: true, // 请求 response_format=json_object；部分网关不支持可置 false
  },
  bot: {
    name: 'AI阿花',
    join: { mode: 'fixed', deskId: 1, posId: 3 }, // mode: 'fixed' | 'quickJoin'
    autoPrepare: true,
    host: { autoStart: true, fillBots: false },
    chat: { onOwnTurn: true, onOthersTurn: false, minIntervalMs: 5000 },
    decideTimeoutMs: 30000,
    reconnect: { enabled: true, maxDelayMs: 10000 },
    keepPlaying: true, // 对局结束后自动重新准备、等待下一局
    verbose: false,
  },
};

function isPlainObject(v) {
  return v && typeof v === 'object' && !Array.isArray(v);
}

function deepMerge(base, over) {
  const out = Array.isArray(base) ? base.slice() : { ...base };
  for (const key of Object.keys(over || {})) {
    if (isPlainObject(base[key]) && isPlainObject(over[key])) {
      out[key] = deepMerge(base[key], over[key]);
    } else if (over[key] !== undefined) {
      out[key] = over[key];
    }
  }
  return out;
}

// 规范化 server.url：允许填 http/https，统一转 ws/wss 并带上 /ws 路径。
function normalizeWsUrl(url) {
  if (!url) return url;
  let u = String(url).trim();
  if (u.startsWith('http://')) u = 'ws://' + u.slice('http://'.length);
  else if (u.startsWith('https://')) u = 'wss://' + u.slice('https://'.length);
  if (!/\/ws[?#/]?$/.test(u)) u = u.replace(/\/+$/, '') + '/ws';
  return u;
}

// 服务器 / LLM 环境变量覆盖（对所有 bot 生效）。
function applySharedEnv(merged) {
  if (process.env.TALKCARDS_WS_URL) merged.server.url = process.env.TALKCARDS_WS_URL;
  if (process.env.OPENAI_BASE_URL) merged.llm.baseUrl = process.env.OPENAI_BASE_URL;
  if (process.env.OPENAI_API_KEY) merged.llm.apiKey = process.env.OPENAI_API_KEY;
  if (process.env.OPENAI_MODEL) merged.llm.model = process.env.OPENAI_MODEL;
}

// 单个 bot 的 bot.* 环境变量覆盖（仅单 bot 模式生效，多 bot 时名字/入座由配置显式给出）。
function applyBotEnv(merged) {
  if (process.env.TALKCARDS_BOT_NAME) merged.bot.name = process.env.TALKCARDS_BOT_NAME;
  if (process.env.TALKCARDS_JOIN_MODE) merged.bot.join.mode = process.env.TALKCARDS_JOIN_MODE;
  if (process.env.TALKCARDS_DESK_ID != null && process.env.TALKCARDS_DESK_ID !== '') {
    merged.bot.join.deskId = parseInt(process.env.TALKCARDS_DESK_ID, 10);
  }
  if (process.env.TALKCARDS_POS_ID != null && process.env.TALKCARDS_POS_ID !== '') {
    merged.bot.join.posId = parseInt(process.env.TALKCARDS_POS_ID, 10);
  }
  if (process.env.TALKCARDS_HOST_AUTOSTART != null && process.env.TALKCARDS_HOST_AUTOSTART !== '') {
    merged.bot.host.autoStart = process.env.TALKCARDS_HOST_AUTOSTART !== '0' &&
      process.env.TALKCARDS_HOST_AUTOSTART.toLowerCase() !== 'false';
  }
  if (process.env.TALKCARDS_HOST_FILLBOTS != null && process.env.TALKCARDS_HOST_FILLBOTS !== '') {
    merged.bot.host.fillBots = process.env.TALKCARDS_HOST_FILLBOTS === '1' ||
      process.env.TALKCARDS_HOST_FILLBOTS.toLowerCase() === 'true';
  }
}

function readFileConfig() {
  const cfgPath = path.join(__dirname, 'ai-config.json');
  if (!fs.existsSync(cfgPath)) {
    console.warn('[config] 未找到 ai-config.json，使用默认配置（可复制 ai-config.example.json 修改）');
    return {};
  }
  try {
    return JSON.parse(fs.readFileSync(cfgPath, 'utf8'));
  } catch (e) {
    console.error('[config] ai-config.json 解析失败，退回默认配置：', e.message);
    return {};
  }
}

// 返回本次要启动的全部 bot 配置数组（长度 ≥ 1）。
function loadConfigs() {
  const fileCfg = readFileConfig();
  const bots = Array.isArray(fileCfg.bots) && fileCfg.bots.length ? fileCfg.bots : null;

  // 共享部分：server / llm 默认值 <- 配置文件顶层 server / llm。
  const shared = {};
  if (isPlainObject(fileCfg.server)) shared.server = fileCfg.server;
  if (isPlainObject(fileCfg.llm)) shared.llm = fileCfg.llm;
  const base = deepMerge(DEFAULTS, shared);

  const finalize = (merged) => { merged.server.url = normalizeWsUrl(merged.server.url); return merged; };

  if (bots) {
    return bots.map((b) => {
      const merged = deepMerge(base, { bot: deepMerge(DEFAULTS.bot, b) });
      applySharedEnv(merged);
      return finalize(merged);
    });
  }

  // 单 bot：兼容旧的顶层 "bot" 配置。
  const merged = deepMerge(DEFAULTS, fileCfg);
  applySharedEnv(merged);
  applyBotEnv(merged);
  return [finalize(merged)];
}

module.exports = {
  loadConfigs,
  // 向后兼容的单个 bot 加载（等价于 loadConfigs()[0]）。
  loadConfig: () => loadConfigs()[0],
  DEFAULTS,
};