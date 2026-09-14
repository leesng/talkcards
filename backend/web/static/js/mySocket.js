(function (global) {
  'use strict';

  function connect(url) {
    var handlers = {};
    var queue = [];
    var ws = null;
    var closed = false;
    var retry = 0;
    var lastLogin = null; // last LOGIN payload, re-sent after reconnect
    var that = {};

    function open() {
      ws = new WebSocket(url);
      ws.onopen = function () {
        retry = 0;
        while (queue.length) { ws.send(queue.shift()); }
        // The server connection is stateless: re-login automatically after reconnect.
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
        var delay = Math.min(1000 * Math.pow(2, retry++), 10000);
        setTimeout(open, delay);
      };
      ws.onerror = function () { /* onclose always fires after onerror; handled there */ };
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
