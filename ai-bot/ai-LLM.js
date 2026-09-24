// ai-LLM.js OpenAI 兼容接口（chat/completions）。
// 运行在 Node 进程，非浏览器；依赖 Node 18+ 内置 fetch / AbortController。
'use strict';

// 从任意文本中稳健地取出第一个 JSON 对象（剥离 ```json 代码块、前后杂文）。
function extractJSON(text) {
  if (typeof text !== 'string' || !text) return null;
  let s = text.trim();
  const fence = s.match(/```(?:json)?\s*([\s\S]*?)```/i);
  if (fence) s = fence[1].trim();
  const start = s.indexOf('{');
  const end = s.lastIndexOf('}');
  if (start === -1 || end <= start) return null;
  try {
    return JSON.parse(s.slice(start, end + 1));
  } catch (e) {
    return null;
  }
}

// 发起一次 chat completion，返回助手的文本内容。
// cfg: { baseUrl, apiKey, model, timeoutMs, temperature }
// opts: { temperature, maxTokens, json (bool, 默认 true), responseFormat, body }
async function chatCompletion(cfg, messages, opts = {}) {
  const { baseUrl, apiKey, model, timeoutMs = 30000 } = cfg;
  const temperature = opts.temperature != null ? opts.temperature : (cfg.temperature != null ? cfg.temperature : 0.2);
  const url = String(baseUrl).replace(/\/+$/, '') + '/chat/completions';
  const body = { model, messages, temperature };
  const jsonMode = opts.json != null ? opts.json : cfg.jsonMode !== false;
  if (jsonMode && opts.responseFormat !== false) body.response_format = { type: 'json_object' };
  if (cfg.reasoningEffort) body.reasoning_effort = cfg.reasoningEffort;
  if (opts.maxTokens) body.max_tokens = opts.maxTokens;
  if (opts.body) Object.assign(body, opts.body);

  const ctrl = new AbortController();
  const timer = setTimeout(() => ctrl.abort(), timeoutMs);
  try {
    const res = await fetch(url, {
      method: 'POST',
      headers: {
        'Content-Type': 'application/json',
        ...(apiKey ? { Authorization: 'Bearer ' + apiKey } : {}),
      },
      body: JSON.stringify(body),
      signal: ctrl.signal,
    });
    const text = await res.text();
    if (!res.ok) {
      const err = new Error('LLM HTTP ' + res.status + ': ' + text.slice(0, 500));
      err.status = res.status;
      throw err;
    }
    let data;
    try {
      data = JSON.parse(text);
    } catch (e) {
      throw new Error('LLM 返回非 JSON：' + text.slice(0, 300));
    }
    const content = data && data.choices && data.choices[0] && data.choices[0].message && data.choices[0].message.content;
    if (typeof content !== 'string') throw new Error('LLM 响应缺少 choices[0].message.content');
    return content;
  } finally {
    clearTimeout(timer);
  }
}

// 发起一次 chat completion 并解析为 JSON 对象；解析失败抛错。
async function chatJSON(cfg, messages, opts = {}) {
  const content = await chatCompletion(cfg, messages, opts);
  const obj = extractJSON(content);
  if (!obj) throw new Error('LLM 输出无法解析为 JSON：' + content.slice(0, 200));
  return obj;
}

module.exports = { chatCompletion, chatJSON, extractJSON };