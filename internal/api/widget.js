/* SambaCRM AI-agent embeddable widget — self-contained, no build step.
 * Вставка одной строкой:
 *   <script src="https://agent.sambacrm.online/widget.js"
 *           data-agent="my-agent-slug" data-color="#7c3aed"
 *           data-position="right" data-greeting="Чем помочь?"></script>
 * Контракт рантайма: POST /chat/start -> {conversation_id, sse_token};
 *   GET /chat/sse?token= (SSE data:{type,text}); POST /chat/message {token,text}.
 */
(function () {
  "use strict";
  var script = document.currentScript;
  if (!script) return;

  var agentSlug = script.dataset.agent;
  if (!agentSlug) {
    console.error("[sambacrm-widget] data-agent required");
    return;
  }
  var color = script.dataset.color || "#7c3aed";
  var position = script.dataset.position === "left" ? "left" : "right";
  var greeting = script.dataset.greeting || "";
  // apiBase: data-api или хост, с которого загружен сам скрипт.
  var apiBase = script.dataset.api || new URL(script.src).origin;

  // Стабильный идентификатор посетителя — чтобы при перезагрузке страницы диалог
  // продолжался тем же (сервер делает find-or-create по external_user_id), а оператор/
  // админка видели один непрерывный диалог. В приватном режиме localStorage может
  // бросать — тогда работаем без персистентности (новый id на сессию).
  var STORE_KEY = "scw:uid:" + agentSlug;
  function getUID() {
    try {
      var v = localStorage.getItem(STORE_KEY);
      if (v) return v;
      var nv = (window.crypto && crypto.randomUUID)
        ? crypto.randomUUID()
        : "u-" + Date.now() + "-" + Math.random().toString(16).slice(2);
      localStorage.setItem(STORE_KEY, nv);
      return nv;
    } catch (_) { return null; }
  }
  var uid = getUID();

  var state = { open: false, token: null, streaming: false, messages: [], es: null, form: null };

  var root = document.createElement("div");
  document.body.appendChild(root);
  var shadow = root.attachShadow({ mode: "open" });

  var style = document.createElement("style");
  style.textContent = css(color, position);
  shadow.appendChild(style);

  var bubble = document.createElement("button");
  bubble.className = "scw-bubble";
  bubble.setAttribute("aria-label", "Открыть чат");
  bubble.textContent = "💬";
  bubble.onclick = function () {
    state.open = !state.open;
    // При открытии подключаем SSE заранее: оператор сможет написать клиенту даже
    // до того, как тот отправит первое сообщение (find-or-create диалога по uid).
    if (state.open) ensureSession();
    render();
  };
  shadow.appendChild(bubble);

  var win = document.createElement("div");
  win.className = "scw-window";
  shadow.appendChild(win);

  function render() {
    win.style.display = state.open ? "flex" : "none";
    if (!state.open) return;
    win.innerHTML =
      '<div class="scw-header"><span>AI-помощник</span><button class="scw-close" aria-label="Закрыть">✕</button></div>' +
      '<div class="scw-msgs"></div>' +
      '<div class="scw-row"><input class="scw-inp" placeholder="Сообщение…"/><button class="scw-send">→</button></div>';
    shadow.querySelector(".scw-close").onclick = function () { state.open = false; render(); };
    var inp = shadow.querySelector(".scw-inp");
    var send = function () {
      var text = inp.value.trim();
      if (!text || state.streaming) return;
      inp.value = "";
      sendMessage(text);
    };
    shadow.querySelector(".scw-send").onclick = send;
    inp.onkeydown = function (e) { if (e.key === "Enter") send(); };
    renderMessages();
  }

  function renderMessages() {
    var box = shadow.querySelector(".scw-msgs");
    if (!box) return;
    var html = "";
    if (greeting && state.messages.length === 0) {
      html += '<div class="scw-msg scw-ai">' + esc(greeting) + "</div>";
    }
    for (var i = 0; i < state.messages.length; i++) {
      var m = state.messages[i];
      var cls = m.role === "user" ? "scw-user" : "scw-ai";
      var pre = "";
      if (m.role === "operator") { cls = "scw-ai scw-op"; pre = '<span class="scw-op-tag">Оператор</span>'; }
      if (m.role === "file") {
        var label = m.content ? esc(m.content) + "<br>" : "";
        html += '<div class="scw-msg scw-ai">' + label +
          '<a class="scw-file" href="' + esc(m.url) + '" target="_blank" rel="noopener" download>📄 ' + esc(m.filename) + "</a></div>";
        continue;
      }
      html += '<div class="scw-msg ' + cls + '">' + pre + esc(m.content) + "</div>";
    }
    if (state.form) html += renderForm(state.form);
    box.innerHTML = html;
    bindForm();
    box.scrollTop = box.scrollHeight;
  }

  function renderForm(form) {
    if (form.saved) {
      return '<div class="scw-form scw-form-saved"><strong>Данные сохранены</strong>' +
        '<span>Телефон и email не отправлялись AI-помощнику.</span></div>';
    }
    var html = '<form class="scw-form"><strong>Заполните данные</strong>' +
      '<span class="scw-form-note">Ответы сохранятся отдельно от переписки. Телефон и email не попадут в AI.</span>';
    for (var i = 0; i < form.fields.length; i++) {
      var f = form.fields[i];
      var value = form.values[f.key] == null ? "" : String(form.values[f.key]);
      html += '<label><span>' + esc(f.label) + (f.required ? ' <b aria-hidden="true">*</b>' : '') + '</span>';
      if (f.type === "multiline") {
        html += '<textarea data-field="' + esc(f.key) + '"' + (f.required ? " required" : "") + '>' + esc(value) + '</textarea>';
      } else if (f.type === "enum") {
        html += '<select data-field="' + esc(f.key) + '"' + (f.required ? " required" : "") + '><option value="">Выберите</option>';
        for (var j = 0; j < (f.options || []).length; j++) {
          var option = String(f.options[j]);
          html += '<option value="' + esc(option) + '"' + (option === value ? " selected" : "") + '>' + esc(option) + '</option>';
        }
        html += '</select>';
      } else if (f.type === "boolean") {
        html += '<select data-field="' + esc(f.key) + '"' + (f.required ? " required" : "") + '><option value="">Выберите</option>' +
          '<option value="true"' + (value === "true" ? " selected" : "") + '>Да</option>' +
          '<option value="false"' + (value === "false" ? " selected" : "") + '>Нет</option></select>';
      } else {
        var type = f.type === "phone" ? "tel" : f.type === "email" ? "email" : f.type === "number" ? "number" : f.type === "date" ? "date" : "text";
        html += '<input data-field="' + esc(f.key) + '" type="' + type + '" value="' + esc(value) + '"' + (f.required ? " required" : "") + ' />';
      }
      if (f.why) html += '<small>' + esc(f.why) + '</small>';
      html += '</label>';
    }
    var privacyURL = form.consent && form.consent.privacy_url
      ? new URL(form.consent.privacy_url, new URL(apiBase, window.location.href)).href
      : "";
    html += '<label class="scw-consent"><input class="scw-consent-check" type="checkbox" required' +
      (form.consentGranted ? " checked" : "") + '><span>Предоставляя свои контактные данные, я даю согласие на их обработку' +
      (privacyURL ? ' в соответствии с <a href="' + esc(privacyURL) + '" target="_blank" rel="noopener noreferrer">политикой конфиденциальности</a>' : '') +
      '.</span></label>';
    if (form.error) html += '<span class="scw-form-error" role="alert">' + esc(form.error) + '</span>';
    html += '<button type="submit"' + (form.submitting ? " disabled" : "") + '>' + (form.submitting ? "Сохраняем…" : "Сохранить") + '</button></form>';
    return html;
  }

  function bindForm() {
    var formEl = shadow.querySelector(".scw-form:not(.scw-form-saved)");
    if (!formEl || !state.form) return;
    var controls = formEl.querySelectorAll("[data-field]");
    for (var i = 0; i < controls.length; i++) {
      controls[i].oninput = function (e) { state.form.values[e.target.dataset.field] = e.target.value; };
    }
    var consent = formEl.querySelector(".scw-consent-check");
    if (consent) consent.onchange = function (e) { state.form.consentGranted = e.target.checked; };
    formEl.onsubmit = function (e) {
      e.preventDefault();
      if (!formEl.reportValidity() || state.form.submitting) return;
      submitForm();
    };
  }

  function loadForm() {
    ensureSession().then(function (token) {
      if (!token) return;
      fetch(apiBase + "/chat/intake-form?token=" + encodeURIComponent(token))
        .then(function (r) { return r.ok ? r.json() : null; })
        .then(function (data) {
          if (!data || !data.fields || !data.fields.length) return;
          state.form = { fields: data.fields, consent: data.consent, consentGranted: false, values: {}, submitting: false, error: "", saved: false };
          renderMessages();
        });
    });
  }

  function submitForm() {
    state.form.submitting = true;
    state.form.error = "";
    renderMessages();
    var values = {};
    for (var i = 0; i < state.form.fields.length; i++) {
      var f = state.form.fields[i];
      var value = state.form.values[f.key];
      if (value !== undefined && value !== "") values[f.key] = value;
    }
    fetch(apiBase + "/chat/intake-form", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ token: state.token, values: values, consent_granted: state.form.consentGranted })
    }).then(function (r) {
      if (r.ok) return null;
      return r.json().then(function (body) { return body.error || "Не удалось сохранить данные"; });
    }).then(function (error) {
      state.form.submitting = false;
      state.form.error = error || "";
      state.form.saved = !error;
      renderMessages();
    }).catch(function () {
      state.form.submitting = false;
      state.form.error = "Не удалось сохранить данные";
      renderMessages();
    });
  }

  function sendMessage(text) {
    state.messages.push({ role: "user", content: text });
    state.messages.push({ role: "assistant", content: "" });
    state.streaming = true;
    renderMessages();

    ensureSession().then(function (token) {
      if (!token) { state.streaming = false; return; }
      fetch(apiBase + "/chat/message", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ token: token, text: text })
      });
    });
  }

  function ensureSession() {
    if (state.token) return Promise.resolve(state.token);
    return fetch(apiBase + "/chat/start", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ agent_slug: agentSlug, external_user_id: uid || "", consent_granted: true })
    })
      .then(function (r) { return r.ok ? r.json() : null; })
      .then(function (data) {
        if (!data) return null;
        state.token = data.sse_token;
        openSSE(data.sse_token);
        loadHistory(data.sse_token);
        return state.token;
      });
  }

  // loadHistory восстанавливает историю диалога при открытии/перезагрузке виджета.
  function loadHistory(token) {
    if (state.messages.length > 0) return; // уже есть локальные сообщения — не затираем
    fetch(apiBase + "/chat/history?token=" + encodeURIComponent(token))
      .then(function (r) { return r.ok ? r.json() : null; })
      .then(function (data) {
        if (!data || !data.messages || !data.messages.length) return;
        if (state.messages.length > 0) return;
        for (var i = 0; i < data.messages.length; i++) {
          var m = data.messages[i];
          state.messages.push({ role: m.role, content: m.content });
        }
        renderMessages();
      })
      .catch(function () { /* история не критична */ });
  }

  function openSSE(token) {
    var es = new EventSource(apiBase + "/chat/sse?token=" + encodeURIComponent(token));
    es.onmessage = function (e) {
      try {
        var ev = JSON.parse(e.data);
        if (ev.type === "text" && ev.text) {
          var last = state.messages[state.messages.length - 1];
          if (last && last.role === "assistant") { last.content += ev.text; renderMessages(); }
        } else if (ev.type === "tool_call" && ev.tool === "request_form") {
          loadForm();
        } else if (ev.type === "operator" && ev.text) {
          // Сообщение живого оператора (live takeover) — отдельным пузырём.
          state.messages.push({ role: "operator", content: ev.text });
          state.streaming = false;
          renderMessages();
        } else if (ev.type === "file" && ev.url) {
          // Файл-вложение (напр. оплаченный документ, F1-1) — пузырь со ссылкой.
          state.messages.push({ role: "file", content: ev.text || "", url: ev.url, filename: ev.filename || "Документ" });
          state.streaming = false;
          renderMessages();
        } else if (ev.type === "done" || ev.type === "error") {
          state.streaming = false;
        }
      } catch (_) { /* keep-alive */ }
    };
    es.onerror = function () { state.streaming = false; };
    state.es = es;
  }

  function esc(s) {
    var d = document.createElement("div");
    d.textContent = s;
    return d.innerHTML;
  }

  function css(c, pos) {
    return (
      ":host{all:initial}" +
      ".scw-bubble{position:fixed;" + pos + ":24px;bottom:24px;width:56px;height:56px;border-radius:50%;background:" + c + ";color:#fff;border:none;box-shadow:0 4px 20px rgba(0,0,0,.2);cursor:pointer;font-size:24px;z-index:999999}" +
      ".scw-window{position:fixed;" + pos + ":24px;bottom:96px;width:360px;height:560px;max-height:80vh;background:#fff;border-radius:12px;box-shadow:0 10px 40px rgba(0,0,0,.2);display:none;flex-direction:column;font-family:-apple-system,BlinkMacSystemFont,Roboto,sans-serif;z-index:999999}" +
      ".scw-header{padding:12px 16px;border-bottom:1px solid #e5e7eb;display:flex;justify-content:space-between;align-items:center;font-weight:600}" +
      ".scw-close{background:none;border:none;cursor:pointer;font-size:18px}" +
      ".scw-msgs{flex:1;overflow-y:auto;padding:16px}" +
      ".scw-msg{margin-bottom:8px;padding:8px 12px;border-radius:12px;max-width:80%;word-wrap:break-word;white-space:pre-wrap}" +
      ".scw-user{background:" + c + ";color:#fff;margin-left:auto}" +
      ".scw-ai{background:#f3f4f6}" +
      ".scw-op{border-left:3px solid " + c + "}" +
      ".scw-op-tag{display:block;font-size:11px;font-weight:600;color:" + c + ";margin-bottom:2px}" +
      ".scw-form{margin:12px 0;padding:14px;border:1px solid #d1d5db;border-radius:10px;background:#f9fafb;display:grid;gap:11px;font-size:14px}" +
      ".scw-form>strong{font-size:15px}.scw-form-note,.scw-form-saved span{font-size:12px;line-height:1.4;color:#4b5563}" +
      ".scw-form label{display:grid;gap:5px}.scw-form label>span{font-weight:500}.scw-form b{color:#b91c1c}" +
      ".scw-form input,.scw-form textarea,.scw-form select{box-sizing:border-box;width:100%;border:1px solid #9ca3af;border-radius:6px;background:#fff;padding:9px;font:inherit;color:#111827}" +
      ".scw-form textarea{min-height:72px;resize:vertical}.scw-form small{color:#6b7280}.scw-form-error{font-size:12px;color:#b91c1c}" +
      ".scw-consent{display:flex!important;grid-template-columns:none!important;align-items:flex-start;gap:8px!important;font-weight:400!important;font-size:12px;line-height:1.45;color:#4b5563}.scw-consent input{width:17px!important;height:17px;margin:1px 0 0;padding:0;flex:none}.scw-consent a{color:" + c + ";text-decoration:underline}" +
      ".scw-form button{justify-self:start;background:" + c + ";color:#fff;border:0;border-radius:6px;padding:9px 14px;font-weight:600;cursor:pointer}.scw-form button:disabled{opacity:.6;cursor:default}" +
      ".scw-form-saved{background:#f0fdf4;border-color:#86efac}.scw-form-saved strong{color:#166534}" +
      ".scw-row{display:flex;padding:12px;border-top:1px solid #e5e7eb;gap:8px}" +
      ".scw-row input{flex:1;border:1px solid #d1d5db;border-radius:6px;padding:8px}" +
      ".scw-row button{background:" + c + ";color:#fff;border:none;padding:8px 16px;border-radius:6px;cursor:pointer}" +
      "@media(max-width:420px){.scw-window{left:12px!important;right:12px!important;bottom:82px;width:auto;height:min(620px,calc(100vh - 100px));max-height:none}.scw-bubble{bottom:16px}}"
    );
  }

  render();
})();
