// Hosted pairing page. The link token comes in the URL fragment (#t=...), which the
// browser never sends to the server; it is read once, removed from the address bar and
// sent only in the X-Pairing-Token header of same-origin API calls.
(function () {
  'use strict';
  var token = '';
  var m = /(?:^|[#&])t=([A-Za-z0-9._-]+)/.exec(location.hash);
  if (m) token = m[1];
  if (history.replaceState) history.replaceState(null, '', location.pathname);

  var $ = function (id) { return document.getElementById(id); };
  var mode = 'qr';
  var finished = false;
  var timer = null;

  function show(id, on) { $(id).hidden = !on; }
  function setStatus(text) { $('status').textContent = text; }
  function setError(text) { $('error').textContent = text || ''; show('error', !!text); }

  // Tell an embedding page what happened (no secrets in the message).
  function notify(type, extra) {
    if (window.parent === window) return;
    var msg = { type: 'litezap:' + type };
    if (extra) for (var k in extra) msg[k] = extra[k];
    try { window.parent.postMessage(msg, '*'); } catch (e) { /* ignore */ }
  }

  function api(method, path, body) {
    var init = { method: method, headers: { 'X-Pairing-Token': token }, cache: 'no-store', credentials: 'omit' };
    if (body !== undefined) {
      init.headers['Content-Type'] = 'application/json';
      init.body = JSON.stringify(body);
    }
    return fetch('api/' + path, init).then(function (r) {
      return r.json().catch(function () { return {}; }).then(function (data) {
        if (!r.ok) {
          var err = new Error((data.error && data.error.message) || ('HTTP ' + r.status));
          err.status = r.status;
          throw err;
        }
        return data;
      });
    });
  }

  function stop(text, type) {
    finished = true;
    if (timer) clearTimeout(timer);
    ['qr-box', 'phone-box', 'start-box'].forEach(function (id) { show(id, false); });
    setStatus(text);
    notify(type);
  }

  function render(s) {
    setError('');
    if (s.done) {
      show('done-box', true);
      stop('Pronto.', 'paired');
      return;
    }
    notify('state', { state: s.state });
    show('start-box', !!s.can_start);
    if (s.can_start) {
      setStatus('A conexão está parada.');
      show('qr-box', false); show('phone-box', false);
      return;
    }
    if (s.state === 'qr_pending') {
      setStatus(mode === 'qr' ? 'Escaneie o código com o WhatsApp.' : 'Peça um código para o seu número.');
      if (s.qr) $('qr').src = s.qr;
      show('qr-box', mode === 'qr' && !!s.qr);
      show('phone-box', mode === 'phone');
    } else if (s.state === 'banned') {
      stop('Este número foi banido pelo WhatsApp.', 'error');
    } else {
      setStatus('Aguardando o servidor… (' + s.state + ')');
      show('qr-box', false);
      show('phone-box', mode === 'phone' && s.state === 'qr_pending');
    }
  }

  function poll() {
    if (finished) return;
    api('GET', 'state').then(render).catch(function (e) {
      if (e.status === 410 || e.status === 401) {
        stop('Este link expirou ou já foi usado. Peça um novo link.', 'expired');
        return;
      }
      setError('Sem resposta do servidor; tentando de novo…');
    }).then(function () {
      if (!finished) timer = setTimeout(poll, 2000);
    });
  }

  function init() {
    if (!token) {
      stop('Link de conexão inválido.', 'expired');
      return;
    }
    $('show-phone').addEventListener('click', function () { mode = 'phone'; show('qr-box', false); show('phone-box', true); });
    $('show-qr').addEventListener('click', function () {
      mode = 'qr';
      show('phone-box', false);
      if (timer) clearTimeout(timer);
      poll();
    });
    $('phone-go').addEventListener('click', function () {
      var btn = $('phone-go');
      btn.disabled = true;
      setError('');
      api('POST', 'phone', { phone: $('phone').value }).then(function (r) {
        var c = String(r.code || '');
        $('code').textContent = c.length === 8 ? c.slice(0, 4) + '-' + c.slice(4) : c;
        show('code', true); show('code-help', true);
      }).catch(function (e) { setError(e.message); }).then(function () { btn.disabled = false; });
    });
    $('start').addEventListener('click', function () {
      var btn = $('start');
      btn.disabled = true;
      api('POST', 'start', {}).catch(function (e) { setError(e.message); }).then(function () { btn.disabled = false; });
    });
    poll();
  }

  if (document.readyState === 'loading') document.addEventListener('DOMContentLoaded', init);
  else init();
})();
