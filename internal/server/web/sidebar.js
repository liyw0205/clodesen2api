(() => {
  window.createSidebar = () => {
    const sidebar = document.getElementById("workspace-sidebar");
    const toggle = document.getElementById("sidebar-toggle");
    const backdrop = document.getElementById("sidebar-backdrop");
    const column = document.querySelector(".app-column");
    const skip = document.querySelector(".skip-link");
    const compact = window.matchMedia("(max-width: 900px)");
    let opened = false;
    let sidebarFocused = false;

    document.addEventListener("focusin", event => { sidebarFocused = sidebar.contains(event.target); });
    document.addEventListener("pointerdown", event => {
      if (!sidebar.contains(event.target)) sidebarFocused = false;
    });

    function close(restoreFocus = true) {
      const wasOpen = opened;
      opened = false;
      sidebar.classList.remove("is-open");
      sidebar.inert = compact.matches;
      if (compact.matches) sidebar.setAttribute("aria-hidden", "true");
      else sidebar.removeAttribute("aria-hidden");
      sidebar.removeAttribute("role");
      sidebar.removeAttribute("aria-modal");
      toggle.setAttribute("aria-expanded", "false");
      backdrop.hidden = true;
      column.inert = false;
      skip.inert = false;
      document.body.classList.remove("navigation-open");
      if (wasOpen && restoreFocus && compact.matches) toggle.focus({ preventScroll: true });
    }

    function open() {
      if (!compact.matches || document.getElementById("dashboard").hidden) return;
      opened = true;
      sidebar.inert = false;
      sidebar.removeAttribute("aria-hidden");
      sidebar.setAttribute("role", "dialog");
      sidebar.setAttribute("aria-modal", "true");
      sidebar.classList.add("is-open");
      backdrop.hidden = false;
      toggle.setAttribute("aria-expanded", "true");
      column.inert = true;
      skip.inert = true;
      document.body.classList.add("navigation-open");
      const selected = sidebar.querySelector('[aria-selected="true"]');
      selected.scrollIntoView({ block: "nearest" });
      selected.focus({ preventScroll: true });
    }

    function syncLayout() {
      const focused = document.activeElement;
      // CSS may hide the focused item before the media-query event runs.
      const returnToToggle = sidebarFocused || sidebar.contains(focused);
      close(false);
      if (document.getElementById("dashboard").hidden) return;
      if (compact.matches && returnToToggle) toggle.focus({ preventScroll: true });
      else if (!compact.matches && focused === toggle) sidebar.querySelector('[aria-selected="true"]').focus({ preventScroll: true });
    }

    toggle.addEventListener("click", open);
    document.getElementById("sidebar-close").addEventListener("click", () => close());
    backdrop.addEventListener("click", () => close());
    sidebar.addEventListener("click", event => {
      if (event.target.closest("[data-view]")) close();
    });
    sidebar.addEventListener("keydown", event => {
      if (!opened) return;
      if (event.key === "Escape") {
        event.preventDefault();
        event.stopPropagation();
        close();
      } else if (event.key === "Tab") {
        const targets = Array.from(sidebar.querySelectorAll("a[href], button:not([disabled])")).filter(node => node.tabIndex >= 0);
        const first = targets[0], last = targets[targets.length - 1];
        if (event.shiftKey && document.activeElement === first) {
          event.preventDefault(); last.focus();
        } else if (!event.shiftKey && document.activeElement === last) {
          event.preventDefault(); first.focus();
        }
      }
    });
    compact.addEventListener("change", syncLayout);
    window.addEventListener("popstate", () => close());
    syncLayout();
    return { close };
  };
})();
