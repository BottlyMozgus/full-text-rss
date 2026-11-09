// rss-scraper admin: optional enhancements only. Pages work without JS.
(function () {
  'use strict';

  // Copy-to-clipboard buttons: <button data-copy="#element-id">
  document.addEventListener('click', function (e) {
    var btn = e.target.closest('[data-copy]');
    if (!btn) return;
    var target = document.querySelector(btn.getAttribute('data-copy'));
    var text = target ? (target.value !== undefined ? target.value : target.textContent) : '';
    if (!text) return;
    var done = function () {
      var prev = btn.textContent;
      btn.textContent = 'Copied';
      setTimeout(function () { btn.textContent = prev; }, 1500);
      btn.blur();
    };
    if (navigator.clipboard && navigator.clipboard.writeText) {
      navigator.clipboard.writeText(text).then(done, done);
    } else {
      // fallback: select text so the user can copy manually
      var r = document.createRange();
      r.selectNodeContents(target);
      var sel = window.getSelection();
      sel.removeAllRanges();
      sel.addRange(r);
      done();
    }
  });

  // Confirm dialogs on destructive forms: <form data-confirm="message">
  document.addEventListener('submit', function (e) {
    var form = e.target;
    if (form.matches('[data-confirm]') && !window.confirm(form.getAttribute('data-confirm'))) {
      e.preventDefault();
    }
  });
})();
