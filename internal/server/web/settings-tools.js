(() => {
  "use strict";
  window.createSettingsTools = ({ element, onGroup }) => {
    const root = document.querySelector("#settings-view");
    const form = document.querySelector("#settings-form");
    const cache = element("section", "settings-section"); cache.id = "cache-settings";
    cache.append(element("h2", "", "缓存刷新周期"));
    const grid = element("div", "settings-grid");
    for (const [name, label] of [["accountsSeconds", "账号与余额（秒）"], ["modelsSeconds", "模型目录（秒）"], ["creditsSeconds", "积分记录（秒）"]]) {
      const field = element("label", "", label), input = element("input");
      input.id = "settings-cache-" + name; input.type = "number"; input.min = 30; input.max = 86400; input.step = 1; input.required = true;
      field.append(input); grid.append(field);
    }
    cache.append(grid); form.querySelector(".settings-actions").before(cache);
    const nav = element("div", "settings-groups"); nav.id = "settings-groups"; nav.setAttribute("role", "tablist"); nav.setAttribute("aria-label", "设置分组");
    const groups = [["general", "基本设置", form], ["operations", "运行状态", document.querySelector("#operations-panel")], ["notifications", "告警通知", document.querySelector("#notifications-panel")], ["admission", "请求准入", document.querySelector("#admission-panel")], ["storage", "存储备份", document.querySelector("#storage-panel")], ["update", "版本更新", root.querySelector(".update-section")]];
    let selected = "general", baseline = null;
    function select(name) {
      selected = name;
      for (const [id, , panel] of groups) { panel.hidden = id !== name; const button = nav.querySelector("[data-group=" + id + "]"); button.setAttribute("aria-selected", String(id === name)); button.tabIndex = id === name ? 0 : -1; }
      onGroup(name);
    }
    for (const [id, label, panel] of groups) {
      panel.id ||= "settings-" + id + "-panel"; panel.setAttribute("role", "tabpanel"); panel.setAttribute("aria-labelledby", "settings-group-" + id);
      const button = element("button", "button button-quiet", label); button.type = "button"; button.dataset.group = id; button.setAttribute("role", "tab"); button.setAttribute("aria-controls", panel.id);
      button.id = "settings-group-" + id;
      button.addEventListener("click", () => select(id)); nav.append(button);
    }
    nav.addEventListener("keydown", event => {
      if (!["ArrowRight", "ArrowLeft", "Home", "End"].includes(event.key)) return;
      event.preventDefault(); const current = groups.findIndex(([id]) => id === selected);
      const index = event.key === "Home" ? 0 : event.key === "End" ? groups.length-1 : (current+(event.key === "ArrowRight" ? 1 : -1)+groups.length)%groups.length;
      select(groups[index][0]); nav.querySelector("[data-group=" + groups[index][0] + "]").focus();
    });
    root.querySelector(".page-heading").after(nav);
    const preview = element("div", "settings-preview"); preview.id = "settings-preview"; preview.setAttribute("role", "status"); preview.hidden = true;
    const pending = element("p", "field-note"); pending.id = "settings-pending"; pending.hidden = true;
    form.querySelector(".settings-actions").prepend(pending, preview);
    const fields = {
      "server.addr": "settings-server-addr", "upstream.baseURL": "settings-base-url", "upstream.openAIBaseURL": "settings-openai-url", "upstream.loginPath": "settings-login-path", "upstream.loginTimeout": "settings-login-timeout", "upstream.requestTimeout": "settings-request-timeout", "upstream.userAgent": "settings-user-agent", "auth.strategy": "settings-strategy", "auth.rpm": "settings-account-rpm", "auth.autoRelogin": "settings-auto-relogin", "auth.creditCooldownThreshold": "settings-credit-cooldown-threshold", "checkin.enable": "settings-checkin-enable", "checkin.path": "settings-checkin-path", "checkin.at": "settings-checkin-at", "checkin.timezone": "settings-checkin-timezone", "cache.accountsSeconds": "settings-cache-accountsSeconds", "cache.modelsSeconds": "settings-cache-modelsSeconds", "cache.creditsSeconds": "settings-cache-creditsSeconds"
    };
    function changes() {
      preview.replaceChildren(); if (!baseline) { preview.hidden = true; return; }
      const list = element("ul");
      for (const [path, id] of Object.entries(fields)) {
        const input = document.getElementById(id), [group, name] = path.split(".");
        const value = input.type === "checkbox" ? input.checked : input.type === "number" ? Number(input.value) : input.value.trim();
        if (value === baseline[group]?.[name]) continue;
        const field = baseline.fields?.[path], parent = input.closest("label");
        const label = input.type === "checkbox" ? parent.querySelector("span:not(.setting-source)").textContent : Array.from(parent.childNodes).filter(node => node.nodeType === Node.TEXT_NODE).map(node => node.textContent).join("").trim();
        const mode = field?.mode || (path.startsWith("cache.") || ["auth.rpm", "auth.strategy", "auth.creditCooldownThreshold", "checkin.enable", "checkin.at"].includes(path) ? "hot" : "restart");
        list.append(element("li", "", label + " · " + (mode === "hot" ? "保存后生效" : "重启后生效")));
      }
      preview.hidden = !list.children.length;
      if (list.children.length) preview.append(element("strong", "", "待保存 " + list.children.length + " 项"), list);
    }
    form.addEventListener("input", changes); form.addEventListener("change", changes);
    select("general");
    return { fields, select, set(settings) {
      baseline = settings;
      for (const name of ["accountsSeconds", "modelsSeconds", "creditsSeconds"]) document.getElementById("settings-cache-" + name).value = settings.cache?.[name] || 3600;
      const count = Object.values(settings.fields || {}).filter(field => field.pending).length;
      pending.hidden = count === 0; pending.textContent = count ? "已保存，" + count + " 项配置待重启生效" : "";
      changes();
    }, cache() { return Object.fromEntries(["accountsSeconds", "modelsSeconds", "creditsSeconds"].map(name => [name, Number(document.getElementById("settings-cache-" + name).value)])); }, clear() {
      baseline = null; form.reset(); preview.replaceChildren(); preview.hidden = true; pending.textContent = ""; pending.hidden = true;
      select("general");
    } };
  };
})();
