   signal: controller.signal
      });
      if (!res.ok) {
        var text = '';
        try { text = (await res.text()).slice(0, 300); } catch (e) {}
        send('__HTTP_ERROR__:' + res.status + ':' + text);
        delete window.__aicodeAbort[requestId];
        return;
      }
      var reader = res.body.getReader();
      var decoder = new TextDecoder();
      var eventName = '';
      var buf = '';
      function dispatch(line) {
        if (line.charAt(line.length - 1) === '\r') { line = line.slice(0, -1); }
        if (line.indexOf('event:') === 0) { eventName = line.slice(6).trim(); return; }
        if (line.indexOf('data:') !== 0) { return; }
        var data = line.slice(5).trim();
        if (!data || data === '{}') { return; }
        try {
          var obj = JSON.parse(data);
          obj._event = eventName;
          send(JSON.stringify(obj));
        } catch (e) {}
      }
      while (true) {
        var chunk = await reader.read();
        if (chunk.done) { break; }
        buf += decoder.decode(chunk.value, { stream: true });
        var idx;
        while ((idx = buf.indexOf('\n')) >= 0) {
          dispatch(buf.slice(0, idx));
          buf = buf.slice(idx + 1);
        }
      }
      if (buf.trim()) { dispatch(buf); }
      send('__DONE__');
    } catch (e) {
      if (e && e.name === 'AbortError') { send('__DONE__'); }
      else { send('__ERROR__:' + ((e && e.message) ? e.message : e)); }
    }
    delete window.__aicodeAbort[requestId];
  };

  window.__aicodeSamantha = async function (url, payloadJson, requestId) {
    var bridge = window.aicodeHost;
    function send(data) {
      try { bridge.onChunk(requestId, data); } catch (e) {}
    }
    try {
      var csrf = '';
      var m = /(?:^|;\s*)passport_csrf_token=([^;]*)/.exec(document.cookie || '');
      if (m && m[1]) { csrf = decodeURIComponent(m[1]); }
      var headers = { 'Content-Type': 'application/json', 'agw-js-conv': 'str' };
      if (csrf) { headers['x-tt-passport-csrf-token'] = csrf; }
      var controller = new AbortController();
      window.__aicodeAbort[requestId] = controller;
      var res = await fetch(url, {
        method: 'POST',
        headers: headers,
        body: payloadJson,
        credentials: 'include',
        signal: controller.signal
      });
      if (!res.ok) {
        var text = '';
        try { text = (await res.text()).slice(0, 300); } catch (e) {}
        send('__HTTP_ERROR__:' + res.status + ':' + te