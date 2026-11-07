(() => {
  "use strict";
  window.createBackupSchedule = ({ api, element, formatTime, parent }) => {
    const form = element("form", "settings-form backup-schedule"); form.id = "backup-schedule-form";
    const grid = element("div", "settings-grid"), inputs = {};
    for (const [name, label, type] of [["enabled", "启用每日备份", "checkbox"], ["at", "备份时间", "time"], ["timezone", "备份时区", "text"], ["retain", "保留自动备份数量", "number"]]) {
      const field = element("label", type === "checkbox" ? "checkbox-setting" : "", type === "checkbox" ? undefined : label), input = element("input");
      input.id = "backup-schedule-" + name; input.type = type;
      if (type !== "checkbox") input.required = true;
      if (type === "number") { input.min = 1; input.max = 60; input.step = 1; }
      field.append(input); if (type === "checkbox") field.append(element("span", "", label)); grid.append(field); inputs[name] = input;
    }
    const status = element("p", "field-note"); status.id = "backup-schedule-status"; status.setAttribute("role", "status");
    const error = element("p", "form-error"); error.id = "backup-schedule-error"; error.setAttribute("role", "alert");
    const save = element("button", "button button-secondary", "保存备份计划"); save.type = "submit";
    const refresh = element("button", "button button-quiet", "刷新备份状态"); refresh.type = "button";
    const actions = element("div", "storage-actions"); actions.append(save, refresh);
    form.append(element("h3", "", "定时备份"), grid, status, error, actions); parent.append(form);
    let generation = 0, current = null;
    function render(view) {
      current = view; error.textContent = "";
      for (const [name, input] of Object.entries(inputs)) if (input.type === "checkbox") input.checked = view.settings[name]; else input.value = view.settings[name];
      const parts = [view.settings.enabled ? "每日 " + view.settings.at + " · " + view.settings.timezone : "每日备份已关闭"];
      if (view.lastSuccess) parts.push("最近成功 " + formatTime(view.lastSuccess));
      if (view.lastWarning) parts.push("备份已保存，但旧自动备份清理失败");
      if (view.lastError) parts.push("最近备份失败" + (view.attempts >= 10 ? " · 等待下一天" : " · 重试 " + formatTime(view.retryAt)));
      if (view.leaseUntil && new Date(view.leaseUntil) > new Date()) parts.push("备份进行中");
      status.textContent = parts.join(" · ");
    }
    async function load() {
      const epoch = generation;
      try { const view = await api("/admin/api/backups/schedule"); if (epoch === generation) render(view); }
      catch (failure) { if (epoch === generation) error.textContent = failure.message; }
    }
    refresh.addEventListener("click", load);
    form.addEventListener("submit", async event => {
      event.preventDefault(); if (!current) return;
      const epoch = generation; save.disabled = true; error.textContent = "";
      try {
        const view = await api("/admin/api/backups/schedule", { method: "PUT", body: JSON.stringify({ revision: current.revision, settings: { enabled: inputs.enabled.checked, at: inputs.at.value, timezone: inputs.timezone.value.trim(), retain: Number(inputs.retain.value) } }) });
        if (epoch === generation) render(view);
      } catch (failure) { if (epoch === generation) { if (failure.status === 409) await load(); error.textContent = failure.message; } }
      finally { if (epoch === generation) save.disabled = false; }
    });
    return { load, clear() { generation++; current = null; form.reset(); status.textContent = ""; error.textContent = ""; save.disabled = false; } };
  };
})();
