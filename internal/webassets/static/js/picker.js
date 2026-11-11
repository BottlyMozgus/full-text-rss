// rss-scraper rule picker. Loaded only by the picker page. Without JS the
// page still works: host, content selector and strip selectors are plain
// form fields. See templates/README.md for the behavior contract.
(function () {
  'use strict';

  var root = document.querySelector('[data-picker]');
  if (!root) return;
  var $ = function (sel) { return root.querySelector(sel); };

  var frame = document.getElementById('picker-frame');
  var frameMsg = document.getElementById('picker-frame-msg');
  var form = $('[data-picker-form]');
  if (!frame || !form) return;

  var contentInput = form.elements.content_selector;
  var stripsInput = form.elements.strip_selectors;
  var listEl = $('[data-picker-list]');
  var statusEl = $('[data-picker-status]');
  var lastEl = $('[data-picker-lastpick]');
  var lastSelEl = $('[data-picker-lastpick-sel]');
  var pv = {
    pane: $('.picker-preview'), mode: $('[data-picker-mode]'), len: $('[data-picker-length]'),
    matches: $('[data-picker-matches]'), err: $('[data-picker-error]'),
    title: $('[data-picker-title]'), html: $('[data-picker-html]')
  };

  var IGNORED = { HTML: 1, HEAD: 1, BODY: 1, SCRIPT: 1, NOSCRIPT: 1, STYLE: 1, LINK: 1, META: 1, IFRAME: 1 };
  var FRAME_CSS =
    '.ftrss-pick-hover{outline:2px dotted #1f6feb !important;outline-offset:0 !important;cursor:crosshair}' +
    '.ftrss-pick-selected{outline:3px solid #15803d !important;outline-offset:0 !important;box-shadow:inset 0 0 0 9999px rgba(21,128,61,.10) !important}' +
    '.ftrss-pick-excluded{outline:3px dashed #dc2626 !important;outline-offset:0 !important;box-shadow:inset 0 0 0 9999px rgba(220,38,38,.14) !important}';

  var doc = null;          // frame document once usable
  var state = { content: '', strips: [] };
  var initial = null;      // prefilled values, for Reset
  var last = null;         // { sel, kind } of the latest pick
  var server = null;       // { sig, data } latest preview response
  var hovered = null;
  var timer = null, seq = 0, ctl = null;

  // ---- state <-> form fields ----

  function readFields() {
    state.content = contentInput.value.trim();
    state.strips = stripsInput.value.split('\n').map(function (s) { return s.trim(); })
      .filter(function (s) { return s; });
  }

  function writeFields() {
    contentInput.value = state.content;
    stripsInput.value = state.strips.length ? state.strips.join('\n') + '\n' : '';
  }

  function sig() { return JSON.stringify(state); }

  function say(msg) { statusEl.textContent = msg; }

  // ---- selector generation ----

  function esc(s) { return window.CSS && CSS.escape ? CSS.escape(s) : s.replace(/[^\w-]/g, '\\$&'); }

  function count(sel) {
    if (!doc) return 0;
    try { return doc.querySelectorAll(sel).length; } catch (e) { return -1; }
  }

  function stableId(id) {
    return id && id.length <= 40 && !/^\d|\d{4,}|^[a-f0-9-]{16,}$|^(ember|react|radix|mui|uid|id|auto|gen)[-_:]?\d|^:/i.test(id);
  }

  var STATE_CLASS = /^(active|current|open|opened|closed|show|shown|hidden|visible|selected|hover|focus|disabled|loaded|loading|lazy|lazyloaded|is-.*|has-.*|js-.*)$/i;

  function stableClasses(el) {
    return Array.prototype.filter.call(el.classList, function (c) {
      return c.length <= 32 && !/^ftrss-pick-/.test(c) && !STATE_CLASS.test(c) &&
        !/\d{3,}/.test(c) && !/^(css|sc|jsx|styled|svelte|_)[-_]/i.test(c) && !(/\d/.test(c) && /[A-Z]/.test(c));
    });
  }

  // Short selector that matches exactly one element, or ''.
  function simpleSelector(el) {
    var tag = el.tagName.toLowerCase(), i, j, s;
    if (stableId(el.id) && count('#' + esc(el.id)) === 1) return '#' + esc(el.id);
    var cl = stableClasses(el).slice(0, 6);
    for (i = 0; i < cl.length; i++) {
      s = tag + '.' + esc(cl[i]);
      if (count(s) === 1) return s;
    }
    for (i = 0; i < cl.length; i++) {
      for (j = i + 1; j < cl.length; j++) {
        s = tag + '.' + esc(cl[i]) + '.' + esc(cl[j]);
        if (count(s) === 1) return s;
      }
    }
    return count(tag) === 1 ? tag : '';
  }

  function structSegment(el) {
    var tag = el.tagName.toLowerCase(), n = 0, same = 0, sib = el.parentElement ? el.parentElement.children : [];
    for (var i = 0; i < sib.length; i++) {
      if (sib[i].tagName === el.tagName) { same++; if (sib[i] === el) n = same; }
    }
    return same > 1 ? tag + ':nth-of-type(' + n + ')' : tag;
  }

  function selectorFor(el) {
    var direct = simpleSelector(el);
    if (direct) return direct;
    var parts = [], cur = el, anchor;
    for (var depth = 0; cur && !IGNORED[cur.tagName]; depth++, cur = cur.parentElement) {
      if (depth > 0 && (anchor = simpleSelector(cur))) return [anchor].concat(parts).join(' > ');
      parts.unshift(structSegment(cur));
      if (depth < 7 && count(parts.join(' > ')) === 1) return parts.join(' > ');
    }
    return parts.join(' > ');
  }

  // ---- marking in the frame ----

  function mark() {
    if (!doc) return;
    var old = doc.querySelectorAll('.ftrss-pick-selected,.ftrss-pick-excluded');
    Array.prototype.forEach.call(old, function (e) { e.classList.remove('ftrss-pick-selected', 'ftrss-pick-excluded'); });
    var apply = function (sel, cls) {
      if (!sel) return;
      try {
        Array.prototype.forEach.call(doc.querySelectorAll(sel), function (e) { e.classList.add(cls); });
      } catch (err) { /* invalid selector: shown in the list */ }
    };
    apply(state.content, 'ftrss-pick-selected');
    state.strips.forEach(function (s) { apply(s, 'ftrss-pick-excluded'); });
  }

  // ---- selector list ----

  function row(kind, sel, idx, matches, error) {
    var li = document.createElement('div');
    li.className = 'picker-sel picker-sel-' + kind;
    var badge = document.createElement('span');
    badge.className = 'badge ' + (kind === 'include' ? 'badge-extracted' : 'badge-error');
    badge.textContent = kind === 'include' ? 'Include' : 'Exclude';
    var code = document.createElement('code');
    code.textContent = sel;
    var n = document.createElement('span');
    n.className = 'picker-sel-count small' + (matches === 0 || error ? ' picker-warn' : ' muted');
    n.textContent = error ? 'invalid' : matches === 0 ? 'no match' : matches + (matches === 1 ? ' match' : ' matches');
    var find = document.createElement('button');
    find.type = 'button'; find.className = 'btn btn-sm'; find.textContent = 'Find';
    find.setAttribute('aria-label', 'Scroll to ' + kind + ' selector ' + sel);
    find.addEventListener('click', function () {
      var el = doc && count(sel) > 0 ? doc.querySelector(sel) : null;
      if (el) el.scrollIntoView({ block: 'center' }); else say('That selector matches nothing in the article.');
    });
    var rm = document.createElement('button');
    rm.type = 'button'; rm.className = 'btn btn-sm'; rm.textContent = 'Remove';
    rm.setAttribute('aria-label', 'Remove ' + kind + ' selector ' + sel);
    rm.addEventListener('click', function () {
      if (kind === 'include') state.content = ''; else state.strips.splice(idx, 1);
      changed('Removed ' + sel + '.');
      listEl.focus();
    });
    var top = document.createElement('div');
    top.className = 'picker-sel-row';
    [badge, code, n, find, rm].forEach(function (e) { top.appendChild(e); });
    li.appendChild(top);
    if (error) {
      var p = document.createElement('p');
      p.className = 'picker-sel-err'; p.textContent = error;
      li.appendChild(p);
    }
    return li;
  }

  function renderList() {
    var live = server && server.sig === sig() ? server.data : null;
    listEl.textContent = '';
    if (state.content) {
      var cm = live ? live.content_matches : count(state.content);
      listEl.appendChild(row('include', state.content, 0, cm, (live && live.content_error) || (count(state.content) < 0 ? 'Not a valid selector.' : '')));
    }
    state.strips.forEach(function (s, i) {
      var m = live && live.strip_matches && live.strip_matches[i] !== undefined ? live.strip_matches[i] : count(s);
      var err = (live && live.strip_errors && live.strip_errors[i]) || (count(s) < 0 ? 'Not a valid selector.' : '');
      listEl.appendChild(row('exclude', s, i, m, err));
    });
    if (!listEl.childNodes.length) {
      var p = document.createElement('p');
      p.className = 'muted small'; p.textContent = 'Nothing picked yet. Readability picks the content until you include an element.';
      listEl.appendChild(p);
    }
    if (last) { lastSelEl.textContent = last.sel; lastEl.hidden = false; } else { lastEl.hidden = true; }
  }

  // ---- preview ----

  function showPreview(data, fail) {
    pv.pane.hidden = false;
    var ok = !fail && !data.error;
    pv.err.hidden = ok;
    pv.err.textContent = ok ? '' : (fail || data.error);
    pv.mode.hidden = !ok;
    pv.len.textContent = '';
    pv.matches.hidden = true;
    pv.html.textContent = '';
    pv.title.hidden = true;
    if (!ok) return;
    pv.mode.textContent = data.mode;
    pv.mode.className = 'badge ' + (data.mode === 'rule' ? 'badge-extracted' : data.mode === 'readability' ? 'badge-pending' : 'badge-fallback');
    pv.len.textContent = data.text_length + ' characters';
    if (state.content) {
      pv.matches.hidden = false;
      pv.matches.textContent = data.content_error ? 'Content selector ignored: ' + data.content_error :
        'Content selector matched ' + data.content_matches + (data.content_matches === 1 ? ' element.' : ' elements.') +
        (data.content_matches === 0 ? ' Readability was used instead.' : '');
    }
    // data.html already passed the server-side bluemonday pipeline; it is not altered here.
    pv.html.innerHTML = data.html;
    var firstHead = pv.html.querySelector('h1,h2');
    var dup = firstHead && data.title && firstHead.textContent.trim() === data.title.trim();
    if (data.title && !dup) { pv.title.textContent = data.title; pv.title.hidden = false; }
    Array.prototype.forEach.call(pv.html.querySelectorAll('a[href]'), function (a) {
      a.target = '_blank'; a.rel = 'noreferrer noopener';
    });
  }

  function runPreview() {
    clearTimeout(timer);
    if (ctl) ctl.abort();
    ctl = window.AbortController ? new AbortController() : null;
    var mine = ++seq, at = sig();
    say('Updating preview...');
    var body = {
      url: form.elements.url.value, content_selector: state.content, strip_selectors: state.strips,
      rewrite_from: form.elements.rewrite_from.value, rewrite_to: form.elements.rewrite_to.value
    };
    fetch(root.getAttribute('data-preview-url'), {
      method: 'POST', credentials: 'same-origin', signal: ctl ? ctl.signal : undefined,
      headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body)
    }).then(function (r) {
      return r.json().catch(function () { return { error: 'Preview failed (HTTP ' + r.status + ').' }; });
    }).then(function (data) {
      if (mine !== seq) return;
      server = { sig: at, data: data };
      showPreview(data);
      renderList();
      say(data.error ? 'Preview failed.' : 'Preview updated.');
    }).catch(function (e) {
      if (mine !== seq || (e && e.name === 'AbortError')) return;
      showPreview(null, 'Preview request failed: ' + e.message);
      say('Preview failed.');
    });
  }

  function changed(msg, delay) {
    writeFields(); mark(); renderList();
    if (msg) say(msg);
    clearTimeout(timer);
    timer = setTimeout(runPreview, delay === undefined ? 300 : delay);
  }

  // ---- picking ----

  function pick(el, kind) {
    var sel = selectorFor(el), at;
    if (kind === 'include') {
      if (state.content === sel) { state.content = ''; last = null; return changed('Removed include ' + sel + '.'); }
      state.content = sel;
    } else {
      if ((at = state.strips.indexOf(sel)) >= 0) { state.strips.splice(at, 1); last = null; return changed('Removed exclude ' + sel + '.'); }
      state.strips.push(sel);
    }
    last = { sel: sel, kind: kind };
    changed((kind === 'include' ? 'Included ' : 'Excluded ') + sel + ' (' + count(sel) + ' in page).');
  }

  function currentKind(e) {
    if (e.altKey || e.shiftKey) return 'exclude';
    var r = root.querySelector('input[name="picker_mode"]:checked');
    return r && r.value === 'exclude' ? 'exclude' : 'include';
  }

  function target(e) {
    var el = e.target;
    return el && el.nodeType === 1 && !IGNORED[el.tagName] ? el : null;
  }

  function setHover(el) {
    if (hovered) hovered.classList.remove('ftrss-pick-hover');
    hovered = el;
    if (el) el.classList.add('ftrss-pick-hover');
  }

  function attach() {
    var d;
    try { d = frame.contentDocument; } catch (e) { d = null; }
    if (!d || !d.documentElement || !/\/admin\/rules\/picker\/frame/.test(d.location.href) || d === doc) return;
    if (d.documentElement.hasAttribute('data-picker-error')) {
      frame.hidden = true;
      frameMsg.hidden = false;
      frameMsg.textContent = (d.body ? d.body.textContent : '').trim() || 'The article could not be opened.';
      say('The article could not be loaded.');
      return;
    }
    doc = d;
    frame.hidden = false;
    frameMsg.hidden = true;
    var st = d.createElement('style');
    st.id = 'ftrss-picker-style';
    st.textContent = FRAME_CSS;
    (d.head || d.documentElement).appendChild(st);
    d.addEventListener('mouseover', function (e) { setHover(target(e)); }, true);
    d.addEventListener('mouseout', function () { setHover(null); }, true);
    d.addEventListener('click', function (e) {
      e.preventDefault(); e.stopPropagation();
      var el = target(e);
      if (el) { setHover(null); pick(el, currentKind(e)); }
    }, true);
    d.addEventListener('submit', function (e) { e.preventDefault(); }, true);
    mark(); renderList();
    runPreview();
  }

  // ---- wiring ----

  Array.prototype.forEach.call(root.querySelectorAll('[data-js-only]'), function (e) { e.hidden = false; });

  readFields();
  initial = { content: state.content, strips: state.strips.slice() };
  renderList();

  [contentInput, stripsInput].forEach(function (inp) {
    inp.addEventListener('input', function () {
      readFields(); last = null; mark(); renderList();
      clearTimeout(timer); timer = setTimeout(runPreview, 600);
    });
  });

  $('[data-picker-preview]').addEventListener('click', runPreview);

  $('[data-picker-reset]').addEventListener('click', function () {
    state.content = initial.content; state.strips = initial.strips.slice();
    var ruleSel = form.elements.rule_id;
    if (ruleSel) ruleSel.value = '';
    last = null; server = null;
    changed('Selectors reset to the saved values.', 0);
  });

  $('[data-picker-parent]').addEventListener('click', function () {
    if (!last || !doc) return;
    var el = null;
    try { el = doc.querySelector(last.sel); } catch (e) { /* ignore */ }
    var up = el && el.parentElement;
    if (!up || IGNORED[up.tagName]) { say('No parent element to select.'); return; }
    if (last.kind === 'include') state.content = '';
    else if (state.strips.indexOf(last.sel) >= 0) state.strips.splice(state.strips.indexOf(last.sel), 1);
    pick(up, last.kind);
  });

  frame.addEventListener('load', attach);
  attach();
})();
