(() => {
  "use strict";
  window.createFloatingPanel = ({ disclosure, panel, width = 420 }) => {
    const trigger = disclosure.querySelector("summary");
    let frame = 0;
    const visible = () => disclosure.open && trigger.getClientRects().length > 0;

    function position() {
      if (!visible()) return;
      const viewport = window.visualViewport;
      const left = (viewport?.offsetLeft || 0) + 12;
      const top = (viewport?.offsetTop || 0) + 12;
      const availableWidth = (viewport?.width || innerWidth) - 24;
      const availableHeight = (viewport?.height || innerHeight) - 24;
      panel.style.width = Math.max(0, Math.min(width, availableWidth)) + "px";
      panel.style.maxHeight = Math.max(0, availableHeight) + "px";
      const anchor = trigger.getBoundingClientRect();
      const bounds = panel.getBoundingClientRect();
      let y = anchor.bottom + 8;
      if (y + bounds.height > top + availableHeight) y = anchor.top - bounds.height - 8;
      panel.style.left = Math.max(left, Math.min(anchor.left, left + availableWidth - bounds.width)) + "px";
      panel.style.top = Math.max(top, Math.min(y, top + availableHeight - bounds.height)) + "px";
    }

    function schedule() {
      if (frame) return;
      frame = requestAnimationFrame(() => { frame = 0; position(); });
    }

    function close(restoreFocus = false) {
      disclosure.open = false;
      trigger.setAttribute("aria-expanded", "false");
      if (restoreFocus) trigger.focus({ preventScroll: true });
    }

    trigger.setAttribute("aria-controls", panel.id);
    trigger.setAttribute("aria-expanded", "false");
    trigger.addEventListener("click", () => {
      // Position before the next paint; the toggle event may be queued later.
      requestAnimationFrame(position);
    });
    disclosure.addEventListener("toggle", () => {
      trigger.setAttribute("aria-expanded", String(disclosure.open));
      position();
    });
    trigger.addEventListener("keydown", event => {
      if (event.key !== "ArrowDown" || trigger.getAttribute("aria-disabled") === "true") return;
      event.preventDefault();
      disclosure.open = true;
      position();
      (panel.querySelector("input:checked:not(:disabled)") || panel.querySelector("input:not(:disabled), button:not(:disabled)"))?.focus({ preventScroll: true });
    });
    document.addEventListener("pointerdown", event => {
      if (visible() && !disclosure.contains(event.target)) close();
    });
    document.addEventListener("focusin", event => {
      if (visible() && !disclosure.contains(event.target)) close();
    });
    document.addEventListener("keydown", event => {
      if (event.key === "Escape" && visible()) {
        event.preventDefault();
        close(true);
      }
    });
    window.addEventListener("resize", schedule);
    document.addEventListener("scroll", schedule, true);
    window.visualViewport?.addEventListener("resize", schedule);
    window.visualViewport?.addEventListener("scroll", schedule);
    const observer = new ResizeObserver(schedule);
    observer.observe(panel);
    observer.observe(trigger);
    return { close, position: schedule };
  };
})();
