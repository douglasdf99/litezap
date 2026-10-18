// <litezap-pair> web component: embeds the hosted pairing page of a LiteZap server.
//
//   <script src="https://zap.example.com/pair/embed.js" defer></script>
//   <litezap-pair url="https://zap.example.com/pair/#t=pl1...."></litezap-pair>
//
// The server must list your site's origin in pairing_frame_ancestors. Events (bubbling,
// composed): "litezap-paired", "litezap-expired", "litezap-state" (detail.state).
(function () {
  'use strict';
  if (!window.customElements || customElements.get('litezap-pair')) return;

  class LitezapPair extends HTMLElement {
    static get observedAttributes() { return ['url']; }

    constructor() {
      super();
      this._onMessage = this._onMessage.bind(this);
      this._root = this.attachShadow({ mode: 'closed' });
      const style = document.createElement('style');
      style.textContent = ':host{display:block;width:100%;max-width:440px;height:560px}iframe{border:0;width:100%;height:100%}';
      this._root.appendChild(style);
    }

    connectedCallback() {
      window.addEventListener('message', this._onMessage);
      this._render();
    }

    disconnectedCallback() { window.removeEventListener('message', this._onMessage); }

    attributeChangedCallback() { if (this.isConnected) this._render(); }

    _render() {
      const raw = this.getAttribute('url') || '';
      let u;
      try { u = new URL(raw, document.baseURI); } catch (e) { return; }
      if (u.protocol !== 'https:' && u.hostname !== 'localhost' && u.hostname !== '127.0.0.1') return;
      if (!/^\/pair\/?$/.test(u.pathname)) return;
      this._origin = u.origin;
      if (this._frame) this._frame.remove();
      const f = document.createElement('iframe');
      f.title = this.getAttribute('title') || 'Conectar WhatsApp';
      f.referrerPolicy = 'no-referrer';
      f.setAttribute('sandbox', 'allow-scripts allow-same-origin');
      f.src = u.href;
      this._frame = f;
      this._root.appendChild(f);
    }

    _onMessage(ev) {
      if (!this._frame || ev.source !== this._frame.contentWindow || ev.origin !== this._origin) return;
      const d = ev.data || {};
      if (typeof d.type !== 'string' || d.type.indexOf('litezap:') !== 0) return;
      const name = 'litezap-' + d.type.slice(8);
      this.dispatchEvent(new CustomEvent(name, { bubbles: true, composed: true, detail: { state: d.state } }));
    }
  }

  customElements.define('litezap-pair', LitezapPair);
})();
