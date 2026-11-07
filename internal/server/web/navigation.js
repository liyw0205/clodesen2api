(() => {
  "use strict";
  window.createNavigation = () => {
    const views = new Set(["accounts", "analytics", "records", "keys", "settings"]);
    const filters = ["model", "account", "type", "status", "key"];
    const owned = ["view", "page", "creditPage", "q", "accountStatus", "accountSort", ...filters];
    const positive = value => /^\d+$/.test(value || "") ? Math.max(1, Math.min(1000000, Number(value))) : 1;
    function read() {
      const params = new URL(location.href).searchParams;
      return {
        view: views.has(params.get("view")) ? params.get("view") : "accounts",
        page: positive(params.get("page")), creditPage: positive(params.get("creditPage")),
        filters: Object.fromEntries(filters.map(name => [name, (params.get(name) || "").slice(0, 256)])),
        accounts: { query: (params.get("q") || "").slice(0, 256), status: params.get("accountStatus") || "all", sort: params.get("accountSort") || "default" },
      };
    }
    function write(value, replace = false) {
      const url = new URL(location.href);
      for (const name of owned) url.searchParams.delete(name);
      const values = { view: value.view === "accounts" ? "" : value.view, page: value.page > 1 ? value.page : "", creditPage: value.creditPage > 1 ? value.creditPage : "", ...value.filters,
        q: value.accounts.query, accountStatus: value.accounts.status === "all" ? "" : value.accounts.status, accountSort: value.accounts.sort === "default" ? "" : value.accounts.sort };
      for (const [name, item] of Object.entries(values)) if (item !== "" && item != null) url.searchParams.set(name, item);
      if (url.href !== location.href) history[replace ? "replaceState" : "pushState"](null, "", url);
    }
    return { read, write };
  };
})();
