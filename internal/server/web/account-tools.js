(() => {
  "use strict";
  window.createAccountTools = ({ api, element, getAccounts, getSelected, accountState, submitJob, onChange, onSelection, onComplete, showToast }) => {
    const section = document.querySelector(".accounts-section");
    const form = element("form", "account-filters"); form.id = "account-filters";
    const input = element("input"); input.id = "account-search"; input.type = "search"; input.placeholder = "账号或 ID"; input.maxLength = 256;
    const status = element("select"); status.id = "account-status";
    for (const [value, text] of [["all", "全部状态"], ["ready", "可用"], ["cooling", "冷却中"], ["error", "状态异常"], ["disabled", "已停用"], ["low", "余额 ≤ 100"]]) status.append(new Option(text, value));
    const sort = element("select"); sort.id = "account-sort";
    for (const [value, text] of [["default", "默认顺序"], ["name", "账号名称"], ["balance_asc", "余额从低到高"], ["balance_desc", "余额从高到低"]]) sort.append(new Option(text, value));
    for (const [text, control] of [["搜索", input], ["状态", status], ["排序", sort]]) { const label = element("label", "", text); label.append(control); form.append(label); }
    const empty = element("p", "field-note", "没有符合筛选条件的账号"); empty.id = "account-filter-empty"; empty.hidden = true;
    const batch = element("form", "account-batch"); batch.id = "account-batch"; batch.hidden = true;
    const count = element("span"); count.id = "account-selected-count"; count.setAttribute("role", "status");
    const action = element("select"); action.id = "account-batch-action"; action.setAttribute("aria-label", "批量操作");
    for (const [value, text] of [["refresh", "刷新所选启用账号"], ["checkin", "签到所选启用账号"], ["enable", "启用所选账号"], ["disable", "停用所选账号"]]) action.append(new Option(text, value));
    const submit = element("button", "button button-secondary", "执行"); submit.type = "submit";
    const clear = element("button", "text-button", "清除选择"); clear.type = "button";
    const error = element("p", "form-error"); error.id = "account-batch-error"; error.setAttribute("role", "alert");
    batch.append(count, action, submit, clear, error);
    section.querySelector(".section-heading").after(form, batch, empty);
    let generation = 0, busy = false;
    function getState() { return { query: input.value.trim(), status: status.value, sort: sort.value }; }
    function setState(value) {
      input.value = value.query || "";
      status.value = Array.from(status.options).some(option => option.value === value.status) ? value.status : "all";
      sort.value = Array.from(sort.options).some(option => option.value === value.sort) ? value.sort : "default";
    }
    function visible() {
      const query = input.value.trim().toLowerCase();
      const items = getAccounts().filter(account => {
        if (query && ![account.identifier, account.id].some(value => String(value || "").toLowerCase().includes(query))) return false;
        const state = accountState(account).className;
        return status.value === "all" || (status.value === "ready" && state === "status-ready") || (status.value === "cooling" && state === "status-cooldown") || (status.value === "error" && state === "status-error") || (status.value === "disabled" && !account.enabled) || (status.value === "low" && account.balance != null && Number(account.balance) <= 100);
      });
      if (sort.value === "name") items.sort((a, b) => a.identifier.localeCompare(b.identifier, "zh-CN"));
      if (sort.value.startsWith("balance_")) items.sort((a, b) => {
        if (a.balance == null) return b.balance == null ? 0 : 1;
        if (b.balance == null) return -1;
        return (Number(a.balance) - Number(b.balance)) * (sort.value === "balance_desc" ? -1 : 1);
      });
      return items;
    }
    function update() {
      const items = visible();
      const selected = getSelected();
      const hidden = Array.from(selected).filter(id => !items.some(account => account.id === id)).length;
      count.textContent = "已选 " + selected.size + " 个" + (hidden ? "（含筛选外 " + hidden + " 个）" : "");
      batch.hidden = selected.size === 0;
      empty.hidden = getAccounts().length === 0 || items.length > 0;
      section.querySelector("#account-count-label").textContent = "显示 " + items.length + " / " + getAccounts().length + " 个账号";
      for (const control of [action, submit, clear]) control.disabled = busy;
    }
    form.addEventListener("submit", event => event.preventDefault());
    input.addEventListener("input", () => onChange(true));
    status.addEventListener("change", () => onChange(false));
    sort.addEventListener("change", () => onChange(false));
    clear.addEventListener("click", () => { getSelected().clear(); onSelection(); });
    batch.addEventListener("submit", async event => {
      event.preventDefault();
      if (busy) return;
      const ids = Array.from(getSelected());
      if (!ids.length) return;
      const epoch = generation, kind = action.value;
      busy = true; error.textContent = ""; update();
      try {
        if (kind === "enable" || kind === "disable") {
          await api("/admin/api/accounts/batch", { method: "POST", body: JSON.stringify({ accountIds: ids, enabled: kind === "enable" }) });
          if (epoch !== generation) return;
          showToast("已更新 " + ids.length + " 个账号");
          await onComplete();
        } else {
          const targets = ids.filter(id => getAccounts().some(account => account.id === id && account.enabled));
          if (!targets.length) throw new Error("所选账号均已停用");
          await submitJob(kind, targets);
        }
      } catch (failure) { if (epoch === generation) error.textContent = failure.message; }
      finally { if (epoch === generation) { busy = false; update(); } }
    });
    return { visible, getState, setState, update, clear() { generation++; busy = false; error.textContent = ""; } };
  };
})();
