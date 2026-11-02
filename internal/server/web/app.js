(() => {
  const $ = (selector, root = document) => root.querySelector(selector);
  const state = {
    accounts: [],
    selected: new Set(),
    selectionReady: false,
    strategy: "round_robin",
    deletingID: "",
    toastTimer: 0,
    refreshTimer: 0,
    availableCredits: null,
    permanentCredits: null,
    temporaryCredits: null,
    accountsRefreshedAt: null,
    busy: false,
    analyticsBusy: false,
    analyticsLoaded: false,
    models: [],
    usage: null,
    creditUsage: null,
    usageFilter: "",
  };

  const loginPanel = $("#login-panel");
  const dashboard = $("#dashboard");
  const loginForm = $("#login-form");
  const loginError = $("#login-error");
  const setupMessage = $("#setup-message");
  const loginToken = $("#admin-token");
  const loginTokenConfirm = $("#admin-token-confirm");
  const loginTokenConfirmWrap = $("#admin-token-confirm-wrap");
  const loginTitle = $("#login-title");
  const loginSubmit = $("#login-submit");
  const accountDialog = $("#account-dialog");
  const accountForm = $("#account-form");
  const redeemDialog = $("#redeem-dialog");
  const redeemForm = $("#redeem-form");
  const confirmDialog = $("#confirm-dialog");
  const resultDialog = $("#result-dialog");

  function element(tag, className, text) {
    const node = document.createElement(tag);
    if (className) node.className = className;
    if (text !== undefined) node.textContent = text;
    return node;
  }

  function setLogin(enabled, authenticated, setupRequired = false) {
    loginPanel.hidden = authenticated;
    dashboard.hidden = !authenticated;
    loginToken.disabled = !enabled;
    if (setupRequired) {
      loginToken.setAttribute("minlength", "6");
      loginToken.setAttribute("maxlength", "20");
    } else {
      loginToken.removeAttribute("minlength");
      loginToken.removeAttribute("maxlength");
    }
    loginTokenConfirm.disabled = !enabled || !setupRequired;
    loginTokenConfirm.required = setupRequired;
    loginTokenConfirmWrap.hidden = !setupRequired;
    loginTitle.textContent = setupRequired ? "设置管理令牌" : "登录";
    loginSubmit.textContent = setupRequired ? "保存并进入后台" : "登录管理后台";
    loginToken.autocomplete = setupRequired ? "new-password" : "current-password";
    setupMessage.hidden = !setupRequired;
    loginForm.querySelector("[type=submit]").disabled = !enabled;
    loginError.textContent = "";
    if (!authenticated) loginToken.focus();
  }

  async function api(path, options = {}) {
    const headers = new Headers(options.headers || {});
    if (options.body && !headers.has("Content-Type")) headers.set("Content-Type", "application/json");
    const response = await fetch(path, {
      credentials: "same-origin",
      cache: "no-store",
      ...options,
      headers,
    });
    let payload = null;
    const contentType = response.headers.get("content-type") || "";
    if (contentType.includes("application/json")) payload = await response.json();
    if (response.status === 401 && path !== "/admin/api/session") {
      setLogin(true, false, false);
      throw new Error("登录状态已过期，请重新登录");
    }
    if (!response.ok) throw new Error(payload && payload.error ? payload.error : "请求失败");
    return payload;
  }

  function showToast(message) {
    const toast = $("#toast");
    toast.textContent = message;
    toast.dataset.visible = "true";
    window.clearTimeout(state.toastTimer);
    state.toastTimer = window.setTimeout(() => { toast.dataset.visible = "false"; }, 2800);
  }

  function formatNumber(value) {
    if (!hasNumber(value)) return "--";
    return new Intl.NumberFormat("zh-CN").format(Number(value));
  }

  function hasNumber(value) {
    return value !== null && value !== undefined && value !== "" && Number.isFinite(Number(value));
  }

  function formatTime(value) {
    if (!value) return "--";
    const date = new Date(value);
    if (Number.isNaN(date.getTime())) return "--";
    return new Intl.DateTimeFormat("zh-CN", { timeZone: "Asia/Shanghai", month: "2-digit", day: "2-digit", hour: "2-digit", minute: "2-digit" }).format(date);
  }

  function formatChinaTime(value) {
    if (!value) return "--";
    const date = new Date(value);
    if (Number.isNaN(date.getTime())) return String(value);
    return new Intl.DateTimeFormat("zh-CN", { timeZone: "Asia/Shanghai", year: "numeric", month: "2-digit", day: "2-digit", hour: "2-digit", minute: "2-digit" }).format(date);
  }

  function formatDuration(value) {
    if (!hasNumber(value)) return "--";
    const milliseconds = Number(value);
    return milliseconds < 1000 ? Math.round(milliseconds) + " 毫秒" : (milliseconds / 1000).toFixed(2) + " 秒";
  }

  function formatPercent(value) {
    if (!hasNumber(value)) return "--";
    return (Number(value) * 100).toFixed(Number(value) >= 0.995 ? 1 : 1) + "%";
  }

  function modelCreated(value) {
    if (!hasNumber(value) || Number(value) <= 0) return "--";
    const date = new Date(Number(value) * 1000);
    return Number.isNaN(date.getTime()) ? "--" : new Intl.DateTimeFormat("zh-CN", { year: "numeric", month: "2-digit", day: "2-digit" }).format(date);
  }

  function modelCredits(model) {
    if (model.billingType === "token") {
      return hasNumber(model.creditsPerRequest) ? "按用量（参考 " + formatNumber(model.creditsPerRequest) + "）" : "按用量";
    }
    if (hasNumber(model.creditsPerRequest)) return formatNumber(model.creditsPerRequest) + " / 次";
    return "未配置";
  }

  function creditAmount(value) {
    if (!hasNumber(value)) return "--";
    const amount = Number(value);
    return (amount > 0 ? "+" : "") + formatNumber(amount);
  }

  function creditDirection(record) {
    if (record.direction === "increase") return "增加";
    if (record.direction === "decrease") return "减少";
    if (record.direction === "unchanged") return "无变化";
    if (!hasNumber(record.amount) || Number(record.amount) === 0) return "无变化";
    return Number(record.amount) > 0 ? "增加" : "减少";
  }

  function creditAmountText(record) {
    return creditDirection(record) + " " + creditAmount(record.amount) + " 积分";
  }

  function creditRecordStatus(record) {
    if (record.type === "refund") return "已退回";
    if (record.status === "completed") return "已完成";
    if (record.status === "refunded") return "已退款";
    return record.status || "--";
  }

  function creditRecordSource(record) {
    if (record.source === "local") return "本地实时";
    if (record.source === "upstream") return "上游校准";
    return "--";
  }

  function selectView(view) {
    const analytics = view === "analytics";
    const settings = view === "settings";
    $("#accounts-view").hidden = analytics || settings;
    $("#analytics-view").hidden = !analytics;
    $("#settings-view").hidden = !settings;
    document.querySelectorAll("[data-view]").forEach(button => {
      const selected = button.dataset.view === view;
      button.setAttribute("aria-selected", String(selected));
      button.tabIndex = selected ? 0 : -1;
    });
    if (analytics && !state.analyticsLoaded) loadAnalytics();
    if (settings) loadSettings();
  }

  function setSettingsForm(settings) {
    $("#settings-server-addr").value = settings.server?.addr || "";
    $("#settings-base-url").value = settings.upstream?.baseURL || "";
    $("#settings-openai-url").value = settings.upstream?.openAIBaseURL || "";
    $("#settings-login-path").value = settings.upstream?.loginPath || "";
    $("#settings-login-timeout").value = settings.upstream?.loginTimeout || "30s";
    $("#settings-request-timeout").value = settings.upstream?.requestTimeout || "0s";
    $("#settings-user-agent").value = settings.upstream?.userAgent || "";
    $("#settings-strategy").value = settings.auth?.strategy || "round_robin";
    $("#settings-auto-relogin").checked = Boolean(settings.auth?.autoRelogin);
    $("#settings-checkin-enable").checked = Boolean(settings.checkin?.enable);
    $("#settings-checkin-path").value = settings.checkin?.path || "";
    $("#settings-checkin-at").value = settings.checkin?.at || "04:00";
    $("#settings-checkin-timezone").value = settings.checkin?.timezone || "Asia/Shanghai";
    $("#settings-redemption-enable").checked = Boolean(settings.redemption?.enable);
    $("#settings-redemption-at").value = settings.redemption?.at || "04:30";
    $("#settings-redemption-code").value = "";
    $("#settings-redemption-note").textContent = settings.redemption?.codeConfigured ? "兑换码已配置，不会回显原文；留空则保留原兑换码。" : "尚未配置兑换码。";
  }

  async function loadSettings() {
    try { setSettingsForm(await api("/admin/api/config")); $("#settings-error").textContent = ""; }
    catch (error) { $("#settings-error").textContent = error.message; }
  }

  async function saveSettings(event) {
    event.preventDefault();
    const button = $("#settings-save");
    button.disabled = true;
    $("#settings-error").textContent = "";
    const payload = { server: { addr: $("#settings-server-addr").value.trim() }, upstream: {
      baseURL: $("#settings-base-url").value.trim(), openAIBaseURL: $("#settings-openai-url").value.trim(),
      loginPath: $("#settings-login-path").value.trim(), loginTimeout: $("#settings-login-timeout").value.trim(),
      requestTimeout: $("#settings-request-timeout").value.trim(), userAgent: $("#settings-user-agent").value.trim(),
    }, auth: { strategy: $("#settings-strategy").value, autoRelogin: $("#settings-auto-relogin").checked }, checkin: {
      enable: $("#settings-checkin-enable").checked, path: $("#settings-checkin-path").value.trim(),
      at: $("#settings-checkin-at").value, timezone: $("#settings-checkin-timezone").value.trim(),
    }, redemption: {
      enable: $("#settings-redemption-enable").checked, at: $("#settings-redemption-at").value,
      code: $("#settings-redemption-code").value.trim(),
    }};
    try { const result = await api("/admin/api/config", { method: "PUT", body: JSON.stringify(payload) }); setSettingsForm(result.settings); showToast(result.restartRequired ? "设置已保存，部分设置需重启生效" : "设置已保存"); }
    catch (error) { $("#settings-error").textContent = error.message; }
    finally { button.disabled = false; }
  }

  function renderModels() {
    const models = state.models;
    const rows = $("#model-rows");
    const cards = $("#model-cards");
    rows.replaceChildren();
    cards.replaceChildren();
    $("#model-count").textContent = models.length ? formatNumber(models.length) + " 个模型" : "没有模型";
    $("#model-table-wrap").hidden = models.length === 0;
    $("#model-cards").hidden = models.length === 0;
    $("#model-empty").hidden = models.length > 0 || !state.analyticsLoaded;
    for (const model of models) {
      const id = model.id || model.name || "未命名模型";
      const provider = model.owned_by || model.provider || "--";
      const created = modelCreated(model.created);
      const credits = modelCredits(model);
      const row = element("tr");
      row.append(element("td", "model-id", id), element("td", "model-credits", credits), element("td", "", provider), element("td", "", created));
      rows.append(row);

      const card = element("article", "model-card");
      card.append(element("strong", "model-card-id", id));
      const details = element("div", "model-card-details");
      details.append(element("span", "model-credits", credits), element("span", "", provider), element("span", "", created));
      card.append(details);
      cards.append(card);
    }
  }

  function renderCreditUsage() {
    const result = state.creditUsage;
    const rows = $("#credit-usage-rows");
    const cards = $("#credit-usage-cards");
    rows.replaceChildren();
    cards.replaceChildren();
    const records = result && Array.isArray(result.records) ? result.records : [];
    $("#credit-usage-count").textContent = result ? formatNumber(records.length) + " 条记录" : "尚未读取";
    $("#credit-usage-empty").hidden = records.length > 0 || !state.analyticsLoaded;
    $("#credit-usage-table-wrap").hidden = records.length === 0;
    $("#credit-usage-cards").hidden = records.length === 0;
    for (const record of records) {
      const amount = hasNumber(record.amount) ? Number(record.amount) : 0;
      const amountClass = amount > 0 ? "credit-positive" : amount < 0 ? "credit-negative" : "credit-neutral";
      const row = element("tr");
      row.append(element("td", "", formatTime(record.createdAt)),
        element("td", "", record.account || "--"), element("td", "model-id", record.model || "--"),
        element("td", amountClass, creditAmountText(record)),
        element("td", "", hasNumber(record.balanceAfter) ? formatNumber(record.balanceAfter) : "--"),
        element("td", "", creditRecordSource(record)), element("td", "", creditRecordStatus(record)));
      rows.append(row);

      const card = element("article", "credit-usage-card");
      const top = element("div", "credit-usage-card-top");
      top.append(element("strong", "model-id", record.model || "--"), element("span", amountClass, creditAmountText(record)));
      const details = element("div", "credit-usage-card-details");
      details.append(element("span", "", record.account || "--"), element("span", "", "余额 " + (hasNumber(record.balanceAfter) ? formatNumber(record.balanceAfter) : "--")), element("span", "", creditRecordSource(record)), element("span", "", creditRecordStatus(record)), element("span", "", formatTime(record.createdAt)));
      card.append(top, details);
      cards.append(card);
    }
  }

  function renderCreditModelStats() {
    const stats = state.creditUsage && Array.isArray(state.creditUsage.modelStats) ? state.creditUsage.modelStats : [];
    const rows = $("#usage-rows");
    const cards = $("#usage-cards");
    rows.replaceChildren(); cards.replaceChildren();
    $("#usage-empty").hidden = stats.length > 0 || !state.analyticsLoaded;
    $("#usage-table-wrap").hidden = stats.length === 0;
    $("#usage-cards").hidden = stats.length === 0;
    $("#usage-title").nextElementSibling.textContent = "按积分流水汇总，包含本地实时记录和上游退款";
    for (const item of stats) {
      const row = element("tr");
      row.append(element("td", "model-id", item.model || "--"), element("td", "", formatNumber(item.requests)), element("td", "", formatNumber(item.creditsUsed) + " 积分"), element("td", "credit-positive", formatNumber(item.creditsRefunded) + " 积分"), element("td", "", formatNumber(item.netCredits) + " 积分"), element("td", "", formatTime(item.lastUsedAt)));
      rows.append(row);
      const card = element("article", "usage-card");
      card.append(element("div", "usage-card-top", item.model || "--"), element("div", "usage-card-counts", "请求 " + formatNumber(item.requests) + " · 扣除 " + formatNumber(item.creditsUsed) + " · 退回 " + formatNumber(item.creditsRefunded) + " · 净消耗 " + formatNumber(item.netCredits)), element("div", "usage-card-details", "最近 " + formatTime(item.lastUsedAt)));
      cards.append(card);
    }
  }

  function renderUsage() {
    const usage = state.usage;
    const rows = $("#usage-rows");
    const cards = $("#usage-cards");
    rows.replaceChildren();
    cards.replaceChildren();
    if (!usage) {
      $("#usage-empty").hidden = !state.analyticsLoaded;
      $("#usage-table-wrap").hidden = true;
      $("#usage-cards").hidden = true;
      return;
    }
    $("#usage-total").textContent = formatNumber(usage.totalRequests);
    $("#usage-successes").textContent = formatNumber(usage.successes);
    $("#usage-failures").textContent = formatNumber(usage.failures);
    $("#usage-success-rate").textContent = hasNumber(usage.totalRequests) && Number(usage.totalRequests) > 0 ?
      formatPercent(Number(usage.successes || 0) / Number(usage.totalRequests)) : "--";
    $("#usage-latency").textContent = formatDuration(usage.averageLatencyMs);
    $("#usage-period").textContent = usage.startedAt ? "进程启动于 " + formatTime(usage.startedAt) : "进程启动以来";
  }

  async function loadAnalytics(force = false) {
    if (state.analyticsBusy || (state.analyticsLoaded && !force)) return;
    state.analyticsBusy = true;
    const button = $("#analytics-refresh");
    button.disabled = true;
    const oldLabel = button.textContent;
    button.textContent = "读取中";
    $("#model-error").hidden = true;
    $("#usage-error").hidden = true;
    $("#credit-usage-error").hidden = true;
    try {
      const results = await Promise.allSettled([
        api("/admin/api/models" + (force ? "?refresh=1" : "")),
        api("/admin/api/usage"),
        api("/admin/api/credit-usage" + (force ? "?refresh=1" : "")),
      ]);
      const modelResult = results[0];
      const usageResult = results[1];
      const creditUsageResult = results[2];
      if (modelResult.status === "fulfilled") {
        state.models = Array.isArray(modelResult.value.data) ? modelResult.value.data : [];
      } else if (modelResult.reason.message === "登录状态已过期，请重新登录") {
        throw modelResult.reason;
      } else {
        $("#model-error").textContent = modelResult.reason.message;
        $("#model-error").hidden = false;
      }
      if (usageResult.status === "fulfilled") {
        state.usage = usageResult.value;
      } else if (usageResult.reason.message === "登录状态已过期，请重新登录") {
        throw usageResult.reason;
      } else {
        $("#usage-error").textContent = usageResult.reason.message;
        $("#usage-error").hidden = false;
      }
      if (creditUsageResult.status === "fulfilled") {
        state.creditUsage = creditUsageResult.value;
      } else if (creditUsageResult.reason.message === "登录状态已过期，请重新登录") {
        throw creditUsageResult.reason;
      } else {
        $("#credit-usage-error").textContent = creditUsageResult.reason.message;
        $("#credit-usage-error").hidden = false;
      }
      state.analyticsLoaded = true;
      renderModels();
      renderUsage();
      renderCreditModelStats();
      renderCreditUsage();
    } catch (error) {
      if (error.message !== "登录状态已过期，请重新登录") showToast(error.message);
    } finally {
      state.analyticsBusy = false;
      button.disabled = false;
      button.textContent = oldLabel;
    }
  }

  function accountState(account) {
    const cooldownUntil = account.cooldownUntil ? new Date(account.cooldownUntil) : null;
    if (!account.enabled) return { text: "已停用", className: "status-paused" };
    if (cooldownUntil && cooldownUntil > new Date()) return { text: "冷却至 " + formatTime(account.cooldownUntil), className: "status-cooldown" };
    if (account.error) return { text: "状态异常", className: "status-error" };
    return { text: "可用", className: "status-ready" };
  }

  function render() {
    const enabled = state.accounts.filter(account => account.enabled);
    const cooling = enabled.filter(account => account.cooldownUntil && new Date(account.cooldownUntil) > new Date());
    const missingBalance = state.accounts.some(account => account.enabled && !hasNumber(account.balance));
    const totalNode = $("#available-credits");
    totalNode.textContent = state.availableCredits === null ? "--" : formatNumber(state.availableCredits);
    $("#permanent-credits").textContent = state.permanentCredits === null ? "--" : formatNumber(state.permanentCredits);
    $("#temporary-credits").textContent = state.temporaryCredits === null ? "--" : formatNumber(state.temporaryCredits);
    $("#enabled-count").textContent = formatNumber(enabled.length);
    $("#cooldown-count").textContent = formatNumber(cooling.length);
    $("#last-updated").textContent = state.accountsRefreshedAt ? formatTime(state.accountsRefreshedAt) : "尚未刷新";
    $("#account-count-label").textContent = state.accounts.length + " 个账号，" + enabled.length + " 个启用";
    $("#balance-note").hidden = !missingBalance;
    $("#loading-state").hidden = true;
    $("#error-state").hidden = true;
    $("#empty-state").hidden = state.accounts.length > 0;
    $("#account-table-wrap").hidden = state.accounts.length === 0;
    $("#account-cards").hidden = state.accounts.length === 0;
    renderStrategies();
    renderTable();
    renderCards();
    updateSelectAll();
  }

  function renderStrategies() {
    document.querySelectorAll("[data-strategy]").forEach(button => {
      button.setAttribute("aria-pressed", String(button.dataset.strategy === state.strategy));
    });
  }

  function quotaText(account) {
    const remaining = account.redemptionRemaining;
    const limit = account.redemptionLimit;
    if (remaining === null || remaining === undefined) return "未知";
    if (limit === null || limit === undefined) return "剩余 " + formatNumber(remaining);
    return formatNumber(remaining) + " / " + formatNumber(limit) + " 次";
  }

  function checkinText(account) {
    if (account.checkedIn === true) return "今日已签到";
    if (account.checkedIn === false) return "待签到";
    return "未知";
  }

  function accountDetail(account) {
    const cooldownUntil = account.cooldownUntil ? new Date(account.cooldownUntil) : null;
    if (cooldownUntil && cooldownUntil > new Date()) return "积分不足，冷却至 " + formatTime(account.cooldownUntil);
    if (account.error) return account.error;
    if (account.redemptionResetAt || account.resetAt) return "兑换码额度重置 " + formatChinaTime(account.redemptionResetAt || account.resetAt);
    return "更新于 " + formatTime(account.refreshedAt);
  }

  function statusBadge(account) {
    const status = accountState(account);
    const badge = element("span", "status-badge " + status.className, status.text);
    return badge;
  }

  function createCheckbox(account, className) {
    const label = element("label", className);
    const checkbox = document.createElement("input");
    checkbox.type = "checkbox";
    checkbox.checked = state.selected.has(account.id);
    checkbox.disabled = !account.enabled;
    checkbox.dataset.selectAccount = account.id;
    checkbox.setAttribute("aria-label", "选择 " + account.identifier + " 用于兑换");
    label.append(checkbox);
    return label;
  }

  function actionButton(label, action, className = "row-action") {
    const button = element("button", className, label);
    button.type = "button";
    button.dataset.action = action;
    return button;
  }

  function renderTable() {
    const body = $("#account-rows");
    body.replaceChildren();
    for (const account of state.accounts) {
      const row = element("tr");
      const selectCell = element("td", "select-col");
      selectCell.append(createCheckbox(account, "account-check"));
      const identityCell = element("td");
      const identity = element("div", "account-identity");
      identity.append(element("strong", "", account.identifier));
      identity.append(element("span", "", accountDetail(account)));
      identityCell.append(identity);
      const balanceCell = element("td", "balance-cell");
      balanceCell.append(element("strong", "", formatNumber(account.balance)));
      balanceCell.append(element("span", "account-secondary", "永久 " + formatNumber(account.permanentBalance) + " · 临时 " + formatNumber(account.temporaryBalance)));
      const quotaCell = element("td", "quota-cell", quotaText(account));
      const stateCell = element("td");
      stateCell.append(statusBadge(account));
      const checkinCell = element("td", "checkin-cell", checkinText(account));
      if (account.checkedIn === true) checkinCell.classList.add("checkin-done");
      if (account.checkedIn === false) checkinCell.classList.add("checkin-pending");
      const actionsCell = element("td", "actions-col");
      const actions = element("div", "row-actions");
      actions.append(actionButton(account.enabled ? "停用" : "启用", "toggle"));
      actions.append(actionButton("编辑", "edit"));
      actions.append(actionButton("移除", "delete", "row-action row-action-danger"));
      actionsCell.append(actions);
      row.append(selectCell, identityCell, balanceCell, quotaCell, stateCell, checkinCell, actionsCell);
      row.dataset.accountId = account.id;
      body.append(row);
    }
  }

  function renderCards() {
    const list = $("#account-cards");
    list.replaceChildren();
    for (const account of state.accounts) {
      const card = element("article", "account-card");
      card.dataset.accountId = account.id;
      const top = element("div", "account-card-top");
      top.append(createCheckbox(account, "account-card-check"));
      const info = element("div", "account-card-info");
      info.append(element("strong", "", account.identifier));
      info.append(element("span", "", accountDetail(account)));
      top.append(info);
      top.append(statusBadge(account));
      const data = element("div", "account-card-data");
      const balance = element("div");
      balance.append(element("label", "", "积分余额"));
      balance.append(element("strong", "", formatNumber(account.balance)));
      balance.append(element("span", "account-secondary", "永久 " + formatNumber(account.permanentBalance) + " · 临时 " + formatNumber(account.temporaryBalance)));
      const quota = element("div");
      quota.append(element("label", "", "兑换额度"));
      quota.append(element("strong", "", quotaText(account)));
      data.append(balance, quota);
      const checkin = element("div", "account-card-checkin");
      checkin.append(element("label", "", "签到状态"));
      const checkinValue = element("strong", account.checkedIn === true ? "checkin-done" : account.checkedIn === false ? "checkin-pending" : "", checkinText(account));
      checkin.append(checkinValue);
      data.append(checkin);
      const bottom = element("div", "account-card-bottom");
      const actions = element("div", "account-card-actions");
      actions.append(actionButton(account.enabled ? "停用" : "启用", "toggle"));
      actions.append(actionButton("编辑", "edit"));
      actions.append(actionButton("移除", "delete", "row-action row-action-danger"));
      bottom.append(element("span", "account-secondary", account.creditType || accountDetail(account)));
      bottom.append(actions);
      card.append(top, data, bottom);
      list.append(card);
    }
  }

  function updateSelectAll() {
    const control = $("#select-all");
    const enabled = state.accounts.filter(account => account.enabled);
    const count = enabled.filter(account => state.selected.has(account.id)).length;
    control.checked = enabled.length > 0 && count === enabled.length;
    control.indeterminate = count > 0 && count < enabled.length;
    control.disabled = enabled.length === 0;
  }

  async function refresh(showLoading = true, force = false) {
    if (state.busy) return;
    state.busy = true;
    const button = $("#refresh-button");
    button.disabled = true;
    const oldLabel = button.textContent;
    button.textContent = "读取中";
    if (showLoading && !state.accounts.length) {
      $("#loading-state").hidden = false;
      $("#account-table-wrap").hidden = true;
      $("#account-cards").hidden = true;
    }
    $("#error-state").hidden = true;
    try {
      const result = await api("/admin/api/accounts" + (force ? "?refresh=1" : ""));
      state.accounts = Array.isArray(result.accounts) ? result.accounts : [];
      state.strategy = result.strategy || "round_robin";
      state.availableCredits = hasNumber(result.availableCredits) ? Number(result.availableCredits) : null;
      state.permanentCredits = hasNumber(result.permanentCredits) ? Number(result.permanentCredits) : null;
      state.temporaryCredits = hasNumber(result.temporaryCredits) ? Number(result.temporaryCredits) : null;
      state.accountsRefreshedAt = result.refreshedAt || (state.accounts[0] && state.accounts[0].refreshedAt) || null;
      const enabledIDs = new Set(state.accounts.filter(account => account.enabled).map(account => account.id));
      for (const id of Array.from(state.selected)) if (!enabledIDs.has(id)) state.selected.delete(id);
      if (!state.selectionReady) {
        state.accounts.filter(account => account.enabled).forEach(account => state.selected.add(account.id));
        state.selectionReady = true;
      }
      render();
      $("#service-state").dataset.state = "online";
      $("#service-state span").textContent = "已连接";
    } catch (error) {
      if (error.message !== "登录状态已过期，请重新登录") {
        $("#error-state").textContent = error.message;
        $("#error-state").hidden = false;
      }
      $("#service-state").dataset.state = "offline";
      $("#service-state span").textContent = "连接异常";
    } finally {
      state.busy = false;
      button.disabled = false;
      button.textContent = oldLabel;
    }
  }

  function openAccountDialog(account = null) {
    accountForm.reset();
    $("#account-id").value = account ? account.id : "";
    $("#account-dialog-title").textContent = account ? "编辑账号" : "添加账号";
    $("#account-identifier").required = !account;
    $("#account-password").required = !account;
    $("#account-identifier").placeholder = account ? "留空保留当前账号" : "";
    $("#account-password").placeholder = account ? "留空保留当前密码" : "";
    $("#account-form-note").textContent = account ? "仅填写需要更换的字段，留空则保留当前值。" : "密码将保存在本机账号数据文件中。";
    $("#account-form-error").textContent = "";
    accountDialog.showModal();
    $("#account-identifier").focus();
  }

  function openRedeemDialog() {
    $("#redeem-form-error").textContent = "";
    $("#redemption-code").value = "";
    const enabled = state.accounts.filter(account => account.enabled);
    if (!state.selected.size) enabled.forEach(account => state.selected.add(account.id));
    const targetList = $("#redeem-targets");
    targetList.replaceChildren();
    for (const account of enabled) {
      const label = element("label", "redeem-target");
      const checkbox = document.createElement("input");
      checkbox.type = "checkbox";
      checkbox.value = account.id;
      checkbox.checked = state.selected.has(account.id);
      const name = element("span", "", account.identifier);
      const balance = element("em", "", hasNumber(account.balance) ? formatNumber(account.balance) + " 积分" : "--");
      label.append(checkbox, name, balance);
      targetList.append(label);
    }
    redeemDialog.showModal();
    $("#redemption-code").focus();
  }

  async function saveAccount(event) {
    event.preventDefault();
    const id = $("#account-id").value;
    const identifier = $("#account-identifier").value.trim();
    const password = $("#account-password").value;
    const button = accountForm.querySelector("[type=submit]");
    const payload = {};
    if (identifier) payload.identifier = identifier;
    if (password) payload.password = password;
    if (!id) {
      payload.identifier = identifier;
      payload.password = password;
    }
    button.disabled = true;
    $("#account-form-error").textContent = "";
    try {
      await api(id ? "/admin/api/accounts/" + encodeURIComponent(id) : "/admin/api/accounts", {
        method: id ? "PATCH" : "POST",
        body: JSON.stringify(payload),
      });
      accountDialog.close();
      showToast(id ? "账号已更新" : "账号已添加");
      await refresh(false);
    } catch (error) {
      $("#account-form-error").textContent = error.message;
    } finally {
      button.disabled = false;
    }
  }

  async function changeEnabled(account, enabled) {
    try {
      await api("/admin/api/accounts/" + encodeURIComponent(account.id), {
        method: "PATCH",
        body: JSON.stringify({ enabled }),
      });
      showToast(enabled ? "账号已启用" : "账号已停用");
      await refresh(false);
    } catch (error) {
      showToast(error.message);
    }
  }

  async function removeAccount() {
    const id = state.deletingID;
    if (!id) return;
    const button = $("#confirm-delete");
    button.disabled = true;
    try {
      await api("/admin/api/accounts/" + encodeURIComponent(id), { method: "DELETE" });
      confirmDialog.close();
      state.selected.delete(id);
      showToast("账号已移除");
      await refresh(false);
    } catch (error) {
      $("#confirm-copy").textContent = error.message;
    } finally {
      button.disabled = false;
    }
  }

  async function updateStrategy(strategy) {
    if (strategy === state.strategy) return;
    const buttons = document.querySelectorAll("[data-strategy]");
    buttons.forEach(button => { button.disabled = true; });
    try {
      const result = await api("/admin/api/settings", { method: "PUT", body: JSON.stringify({ strategy }) });
      state.strategy = result.strategy;
      renderStrategies();
      showToast(strategy === "random" ? "已切换为随机选择" : "已切换为轮询选择");
    } catch (error) {
      showToast(error.message);
    } finally {
      buttons.forEach(button => { button.disabled = false; });
    }
  }

  function openDeleteDialog(account) {
    state.deletingID = account.id;
    $("#confirm-copy").textContent = "确定移除 " + account.identifier + "？该账号将立即从轮询池中删除。";
    confirmDialog.showModal();
  }

  async function redeem(event) {
    event.preventDefault();
    const code = $("#redemption-code").value.trim();
    const ids = Array.from($("#redeem-targets").querySelectorAll("input:checked")).map(input => input.value);
    if (!code) {
      $("#redeem-form-error").textContent = "请输入兑换码";
      return;
    }
    if (!ids.length) {
      $("#redeem-form-error").textContent = "至少选择一个启用账号";
      return;
    }
    const submit = $("#redeem-submit");
    submit.disabled = true;
    submit.textContent = "正在逐账号兑换";
    $("#redeem-form-error").textContent = "";
    try {
      const result = await api("/admin/api/redeem", {
        method: "POST",
        body: JSON.stringify({ code, accountIds: ids }),
      });
      redeemDialog.close();
      showResults(result);
      await refresh(false);
    } catch (error) {
      $("#redeem-form-error").textContent = error.message;
    } finally {
      submit.disabled = false;
      submit.textContent = "开始兑换";
    }
  }

  function showResults(result) {
    $("#result-title").textContent = result.successCount === result.totalCount ? "兑换完成" : "兑换部分完成";
    $("#result-summary").textContent = "成功 " + formatNumber(result.successCount) + " / " + formatNumber(result.totalCount) +
      " 个账号，到账 " + formatNumber(result.totalCredits) + " 积分。失败账号不会自动重试。";
    const list = $("#result-list");
    list.replaceChildren();
    for (const item of result.results || []) {
      const row = element("li");
      const name = element("span", "", item.account || "账号");
      const details = element("span", item.status === "success" ? "result-success" : "result-error", item.message || item.status);
      row.append(name, details);
      list.append(row);
    }
    resultDialog.showModal();
  }

  function handleAccountAction(event) {
    const button = event.target.closest("[data-action]");
    if (!button) return;
    const row = button.closest("[data-account-id]");
    const account = state.accounts.find(item => item.id === row.dataset.accountId);
    if (!account) return;
    if (button.dataset.action === "toggle") changeEnabled(account, !account.enabled);
    if (button.dataset.action === "edit") openAccountDialog(account);
    if (button.dataset.action === "delete") openDeleteDialog(account);
  }

  async function boot() {
    try {
      const session = await api("/admin/api/session");
      setLogin(session.enabled, session.authenticated, session.setupRequired);
      if (session.authenticated) {
        await refresh();
        startRefreshTimer();
      }
    } catch (error) {
      setLogin(false, false);
      loginError.textContent = error.message;
    }
  }

  function startRefreshTimer() {
    window.clearInterval(state.refreshTimer);
    state.refreshTimer = 0;
  }

  loginForm.addEventListener("submit", async event => {
    event.preventDefault();
    const button = loginForm.querySelector("[type=submit]");
    button.disabled = true;
    loginError.textContent = "";
    try {
      const setupRequired = !loginTokenConfirmWrap.hidden;
      await api("/admin/api/session", {
        method: "POST",
        body: JSON.stringify({ token: loginToken.value, confirm: setupRequired ? loginTokenConfirm.value : "" }),
      });
      loginToken.value = "";
      loginTokenConfirm.value = "";
      setLogin(true, true, false);
      await refresh();
      startRefreshTimer();
    } catch (error) {
      loginError.textContent = error.message;
    } finally {
      button.disabled = false;
    }
  });

  $("#logout-button").addEventListener("click", async () => {
    try { await api("/admin/api/session", { method: "DELETE" }); } catch (_) {}
    window.clearInterval(state.refreshTimer);
    setLogin(true, false, false);
  });
  $("#refresh-button").addEventListener("click", () => refresh(true, true));
  $("#analytics-refresh").addEventListener("click", () => loadAnalytics(true));
  $("#settings-refresh").addEventListener("click", loadSettings);
  $("#settings-form").addEventListener("submit", saveSettings);
  document.querySelectorAll("[data-view]").forEach(button => {
    button.addEventListener("click", () => selectView(button.dataset.view));
    button.addEventListener("keydown", event => {
      if (event.key !== "ArrowLeft" && event.key !== "ArrowRight") return;
      event.preventDefault();
      const views = Array.from(document.querySelectorAll("[data-view]")).map(item => item.dataset.view);
      const current = views.indexOf(button.dataset.view);
      const view = views[(current + (event.key === "ArrowRight" ? 1 : views.length - 1)) % views.length];
      selectView(view);
      $("[data-view='" + view + "']").focus();
    });
  });
  $("#account-open").addEventListener("click", () => openAccountDialog());
  $("#empty-add").addEventListener("click", () => openAccountDialog());
  $("#redeem-open").addEventListener("click", openRedeemDialog);
  accountForm.addEventListener("submit", saveAccount);
  redeemForm.addEventListener("submit", redeem);
  $("#confirm-delete").addEventListener("click", removeAccount);
  $("#account-rows").addEventListener("click", handleAccountAction);
  $("#account-cards").addEventListener("click", handleAccountAction);

  document.querySelectorAll("[data-close]").forEach(button => {
    button.addEventListener("click", () => {
      const dialog = document.getElementById(button.dataset.close);
      if (dialog) dialog.close();
    });
  });
  document.querySelectorAll("[data-strategy]").forEach(button => {
    button.addEventListener("click", () => updateStrategy(button.dataset.strategy));
  });

  document.addEventListener("change", event => {
    const checkbox = event.target;
    if (checkbox.matches("[data-select-account]")) {
      if (checkbox.checked) state.selected.add(checkbox.dataset.selectAccount);
      else state.selected.delete(checkbox.dataset.selectAccount);
      updateSelectAll();
      return;
    }
    if (checkbox.id === "select-all") {
      state.accounts.filter(account => account.enabled).forEach(account => {
        if (checkbox.checked) state.selected.add(account.id);
        else state.selected.delete(account.id);
      });
      renderTable();
      renderCards();
      updateSelectAll();
    }
  });
  $("#select-enabled").addEventListener("click", () => {
    $("#redeem-targets").querySelectorAll("input[type=checkbox]").forEach(input => { input.checked = true; });
  });

  window.fetch("/healthz", { cache: "no-store" }).then(response => {
    if (response.ok) {
      $("#service-state").dataset.state = "online";
      $("#service-state span").textContent = "服务在线";
    }
  }).catch(() => {});

  boot();
})();
