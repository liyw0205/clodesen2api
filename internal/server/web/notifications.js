(() => {
  "use strict";
  window.createNotifications = ({ api, element, formatTime }) => {
    const root = element("section", "settings-section"); root.id = "notifications-panel";
    root.append(element("h2", "", "告警通知"));
    const form = element("form", "notification-form");
    const controls = element("fieldset", "notification-controls"); controls.disabled = true;
    const grid = element("div", "settings-grid");
    const channelLabel = element("label", "", "通知渠道"), channel = element("select"); channel.id = "alerts-channel"; channelLabel.append(channel);
    const thresholdLabel = element("label", "", "启用账号总余额低于"), threshold = element("input");
    threshold.id = "alerts-threshold"; threshold.type = "number"; threshold.min = 0; threshold.max = 1000000000000; threshold.step = 1; threshold.required = true; thresholdLabel.append(threshold);
    grid.append(channelLabel, thresholdLabel);
    const enabled = element("input"); enabled.id = "alerts-enabled"; enabled.type = "checkbox";
    const enabledText = element("span");
    const toggle = element("label", "checkbox-setting"); toggle.append(enabled, enabledText);
    const title = element("h3"); title.id = "alerts-channel-title";
    const fields = element("div", "settings-grid notification-fields"); fields.id = "alerts-channel-fields"; fields.setAttribute("aria-labelledby", title.id);
    const actions = element("div", "notification-actions");
    const save = element("button", "button button-primary", "保存通知设置"); save.type = "submit";
    const test = element("button", "button button-secondary", "发送测试通知"); test.id = "alerts-test"; test.type = "button";
    const refresh = element("button", "button button-quiet", "刷新状态"); refresh.id = "alerts-refresh"; refresh.type = "button";
    actions.append(save, test, refresh);
    const status = element("p", "field-note"), result = element("p", "field-note"), error = element("p", "form-error");
    status.id = "alerts-status"; status.setAttribute("role", "status"); result.id = "alerts-test-result"; result.setAttribute("role", "status"); error.id = "alerts-error"; error.setAttribute("role", "alert");
    const pending = element("p", "field-note"); pending.id = "alerts-dirty"; pending.hidden = true; pending.textContent = "有未保存的更改";
    controls.append(grid, toggle, title, fields, actions);
    form.append(controls); root.append(element("p", "field-note notification-events", "余额不足、账号不可用、任务失败、审计异常及恢复通知"), form, pending, status, result, error);
    document.querySelector("#settings-view").append(root);

    let revision = 1, generation = 0, definitions = [], configured = {}, drafts = {}, current = "webhook", dirty = false, latest = null;
    const inputs = new Map();
    function definition() { return definitions.find(item => item.id === current); }
    function draft(id = current) { return drafts[id] ||= { enabled: false, config: {}, clear: new Set() }; }
    function collect() {
      if (!definition()) return;
      const value = draft(); value.enabled = enabled.checked;
      for (const [key, entry] of inputs) {
        value.config[key] = entry.input.value;
        if (entry.clear?.checked) value.clear.add(key); else value.clear.delete(key);
      }
    }
    function setDirty() {
      dirty = true; pending.hidden = false; error.textContent = result.textContent = ""; collect(); updateOptions();
    }
    function updateOptions() {
      for (const option of channel.options) {
        const meta = definitions.find(item => item.id === option.value);
        option.textContent = meta.name + (drafts[meta.id]?.enabled ? " · 已启用" : "");
      }
    }
    function render() {
      fields.replaceChildren(); inputs.clear();
      const meta = definition(); if (!meta) return;
      const value = draft(); channel.value = current;
      title.textContent = meta.name + " 配置"; enabled.checked = value.enabled; enabledText.textContent = "启用 " + meta.name;
      for (const field of meta.fields) {
        const wrapper = element("div", "notification-field"), label = element("label");
        const input = element(field.type === "select" ? "select" : field.type === "textarea" ? "textarea" : "input");
        input.id = "alerts-field-" + field.key; label.htmlFor = input.id; label.textContent = field.label + (field.required ? " *" : "");
        input.dataset.field = field.key;
        if (field.type === "select") for (const option of field.options) input.append(new Option(option, option));
        else if (field.type === "textarea") { input.rows = 4; input.spellcheck = false; wrapper.classList.add("settings-span"); }
        else input.type = field.secret ? "password" : field.type;
        input.autocomplete = field.secret ? "new-password" : "off";
        input.maxLength = 8192;
        input.placeholder = configured[current]?.[field.key] ? "已配置，留空保留" : field.placeholder || "";
        input.value = value.config[field.key] || (field.type === "select" ? field.options[0] : "");
        if (field.type === "number") { input.min = 0; input.max = 10; input.step = 1; }
        const entry = { input, field };
        if (field.secret && configured[current]?.[field.key]) {
          const clearLabel = element("label", "checkbox-setting notification-clear"), clear = element("input");
          clear.type = "checkbox"; clear.dataset.clear = field.key; clear.checked = value.clear.has(field.key);
          clearLabel.append(clear, element("span", "", "清除已保存的" + field.label));
          clear.addEventListener("change", () => { if (clear.checked) input.value = ""; syncRequired(); });
          entry.clear = clear; wrapper.append(clearLabel);
        }
        wrapper.prepend(label, input); fields.append(wrapper); inputs.set(field.key, entry);
      }
      syncRequired(); updateOptions(); renderStatus();
    }
    function syncRequired() {
      for (const [key, { input, field, clear }] of inputs) {
        input.disabled = !!clear?.checked;
        input.required = enabled.checked && field.required && !configured[current]?.[key] && !clear?.checked;
      }
    }
    function renderStatus() {
      if (!latest) return;
      const active = Object.entries(latest.conditions || {}).filter(([, on]) => on).map(([name]) => ({ low_balance: "余额不足", no_accounts: "无可用账号", job_failure: "任务失败", audit_degraded: "审计降级" })[name]);
      const on = Object.values(latest.settings.channels || {}).filter(item => item.enabled).length;
      const delivery = latest.delivery?.[current];
      status.textContent = (on ? "已启用 " + on + " 个渠道" : "已关闭") + " · 待发送 " + latest.pending.length + (active.length ? " · " + active.join("、") : "") + (delivery?.lastSent ? " · 当前渠道最近发送 " + formatTime(delivery.lastSent) : "") + (delivery?.lastError ? " · 当前渠道发送失败，等待重试" : "") + (latest.dropped ? " · 队列丢弃 " + latest.dropped : "") + (latest.pending.some(item => item.attempts >= 10) ? " · 部分消息重试已耗尽，检查配置后重新保存" : "");
    }
    function accept(value) {
      latest = value; revision = value.revision; definitions = value.definitions; configured = value.configured || {}; drafts = {};
      for (const [id, item] of Object.entries(value.settings.channels || {})) drafts[id] = { enabled: item.enabled, config: { ...item.config }, clear: new Set() };
      threshold.value = value.settings.lowBalance;
      channel.replaceChildren(...definitions.map(item => new Option(item.name, item.id)));
      if (!definitions.some(item => item.id === current)) current = definitions[0].id;
      dirty = false; pending.hidden = true; render();
    }
    function clearKeys() { return Object.entries(drafts).flatMap(([id, value]) => [...value.clear].map(key => id + "." + key)); }
    async function load(force = false) {
      if (dirty && !force) return;
      const epoch = ++generation; controls.disabled = save.disabled = true; error.textContent = result.textContent = "";
      try { const value = await api("/admin/api/alerts"); if (epoch === generation) accept(value); }
      catch (failure) { if (epoch === generation) error.textContent = failure.message; }
      finally { if (epoch === generation) { controls.disabled = false; save.disabled = false; } }
    }
    form.addEventListener("submit", async event => {
      event.preventDefault(); collect();
      const epoch = ++generation; controls.disabled = save.disabled = true; error.textContent = result.textContent = "";
      const channels = Object.fromEntries(Object.entries(drafts).map(([id, value]) => [id, { enabled: value.enabled, config: value.config }]));
      try {
        const value = await api("/admin/api/alerts", { method: "PUT", body: JSON.stringify({ revision, settings: { channels, lowBalance: Number(threshold.value) }, clearSecrets: clearKeys() }) });
        if (epoch === generation) { accept(value); result.textContent = "通知设置已保存"; }
      } catch (failure) { if (epoch === generation) error.textContent = failure.message; }
      finally { if (epoch === generation) { controls.disabled = false; save.disabled = false; } }
    });
    test.addEventListener("click", async () => {
      collect();
      for (const [key, { input, field, clear }] of inputs) {
        if (field.required && !input.value && (!configured[current]?.[key] || clear?.checked)) { error.textContent = "请填写" + field.label; input.focus(); return; }
        if (!input.checkValidity()) { input.reportValidity(); return; }
      }
      const epoch = ++generation; controls.disabled = true; test.textContent = "发送中"; error.textContent = result.textContent = "";
      try {
        const value = await api("/admin/api/alerts/test", { method: "POST", timeout: 12000, body: JSON.stringify({ revision, channel: current, config: draft().config, clearSecrets: [...draft().clear].map(key => current + "." + key) }) });
        if (epoch === generation) result.textContent = value.message;
      } catch (failure) { if (epoch === generation) error.textContent = failure.message; }
      finally { if (epoch === generation) { controls.disabled = false; test.textContent = "发送测试通知"; } }
    });
    channel.addEventListener("change", () => { collect(); current = channel.value; error.textContent = result.textContent = ""; render(); });
    enabled.addEventListener("change", syncRequired);
    form.addEventListener("input", event => { if (event.target !== channel) setDirty(); });
    form.addEventListener("change", event => { if (event.target !== channel) setDirty(); });
    refresh.addEventListener("click", () => load(true));
    return { load, clear() {
      generation++; revision = 1; drafts = {}; configured = {}; definitions = []; latest = null; current = "webhook"; dirty = false;
      form.reset(); fields.replaceChildren(); inputs.clear(); channel.replaceChildren(); title.textContent = enabledText.textContent = status.textContent = result.textContent = error.textContent = "";
      pending.hidden = true; controls.disabled = true; save.disabled = false; test.textContent = "发送测试通知";
    } };
  };
})();
