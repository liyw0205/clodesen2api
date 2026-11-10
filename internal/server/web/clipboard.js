(() => {
  "use strict";
  window.copyText = async (value, { target, isCurrent = () => true } = {}) => {
    if (!value || !isCurrent()) throw new Error("没有可复制的内容");
    if (window.isSecureContext && navigator.clipboard?.writeText) {
      try { await navigator.clipboard.writeText(value); return; }
      catch (_) { /* HTTP pages and restricted browsers also need the selection-based path. */ }
    }
    if (!isCurrent()) throw new Error("复制操作已取消");
    const active = document.activeElement;
    const selection = window.getSelection();
    const ranges = selection ? Array.from({ length: selection.rangeCount }, (_, i) => selection.getRangeAt(i).cloneRange()) : [];
    const scroll = { x: window.scrollX, y: window.scrollY };
    const input = target || document.createElement("textarea");
    const start = input.selectionStart, end = input.selectionEnd, direction = input.selectionDirection;
    if (!target) {
      input.value = value;
      input.readOnly = true;
      input.className = "clipboard-buffer";
      // Elements outside a modal are inert, so the temporary input must be inside it.
      (document.querySelector("dialog[open]") || document.body).append(input);
    }
    try {
      input.focus({ preventScroll: true });
      input.select();
      input.setSelectionRange(0, value.length);
      if (!document.execCommand("copy")) throw new Error("浏览器未允许复制");
    } finally {
      if (!target) { input.value = ""; input.remove(); }
      else input.setSelectionRange(start, end, direction);
      if (active?.isConnected) active.focus({ preventScroll: true });
      if (selection) { selection.removeAllRanges(); for (const range of ranges) selection.addRange(range); }
      window.scrollTo(scroll.x, scroll.y);
    }
  };
})();
