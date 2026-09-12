(function (global) {
  'use strict';

  function connect(url) {
    var handlers = {};      // type -> 回调
    var queue = [];         // 未连上时暂存的出站消息
    var ws = null;
    var closed = false;     // 主动 close 后不再重连
    var retry = 0;
    var lastLogin = null;   // 最近一次 LOGIN 载荷（用户名字符串），重连后自动重发
    var that = {};

    function open() {
      ws = new WebSocket(url);
      ws.onopen = function () {
        retry = 0;
        // 把连接建立前积压的消息按序发出
        while (queue.length) { ws.send(queue.shift()); }
        // 服务器为无状态连接：断线即丢失会话，重连后自动重新登录
        if (lastLogin) { that.emit('LOGIN', lastLogin); }
      };
      ws.onmessage = function (ev) {
        var m;
        try { m = JSON.parse(ev.data); } catch (e) { return; }
        if (!m || !m.type) { return; }
        var fn = handlers[m.type];
        if (fn) { fn(m.data); }
      };
      ws.onclose = function () {
        ws = null;
        if (closed) { return; }
        // 指数退避重连
        var delay = Math.min(1000 * Math.pow(2, retry++), 10000);
        setTimeout(open, delay);
      };
      ws.onerror = function () { /* onerror 后必触发 onclose，统一在其处理 */ };
    }

    that.on = function (type, fn) {
      handlers[type] = fn;
      return that;
    };

    that.emit = function (type, data) {
      if (type === 'LOGIN') { lastLogin = data; }
      var msg = { type: type };
      if (data !== undefined) { msg.data = data; }
      var s = JSON.stringify(msg);
      if (ws && ws.readyState === 1) { ws.send(s); }
      else if (!closed) { queue.push(s); }
    };

    that.close = function () {
      closed = true;
      if (ws) { ws.close(); }
    };

    open();
    return that;
  }

  global.mySocket = { connect: connect };
})(window);
