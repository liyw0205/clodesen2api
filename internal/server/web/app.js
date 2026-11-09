(() => {
  const $ = (selector, root = document) => root.querySelector(selector);
  const state = {
    accounts: [],
    accountsLoaded: false,
    modelCooldowns: [],
    modelCooldownsDegraded: false,
    sessionGeneration: 0,
    selected: new Set(),
    strategy: "fixed",
    deletingID: "",
    toastTimer: 0,
    redemptionProgressJobId: "",
    availableCredits: null,
    consumedCredits: null,
    accountsRefreshedAt: null,
    accountsRefreshPromise: null,
    modelsLoaded: false,
    creditsLoaded: false,
    view: "accounts",
    models: [],
    usage: null,
    creditUsage: null,
    creditUsageBusy: false,
    creditUsagePage: 1,
    creditUsagePageSize: 20,
    requestRecords: null,
    requestRecordsController: null,
    requestRecordsRequestID: 0,
    requestRecordsLastRefreshAt: 0,
    requestRecordsPage: 1,
    requestRecordsPageSize: 20,
    requestRecordFilters: { model: "", account: "", type: "", status: "", key: "" },
    updateSourceID: "",
    updateSourceOptions: [],
    updateSourceResults: {},
    updateSourceTesting: false,
    settings: null,
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
  const accountForm = $("#account-form");
  const redeemDialog = $("#redeem-dialog");
  const redeemForm = $("#redeem-form");
  const resultDialog = $("#result-dialog");
  const navigation = window.createNavigation();
  const sidebar = window.createSidebar();
  const apiClient = window.createAdminClient({ onUnauthorized: () => setLogin(true, false, false) });

  function element(tag, className, text) {
    const node = document.createElement(tag);
    if (className) node.className = className;
    if (text !== undefined) node.textContent = text;
    return node;
  }

  function setLogin(enabled, authenticated, setupRequired = false) {
    if (!authenticated) {
      sidebar.close(false);
      state.sessionGeneration++;
      apiClient.clear();
      updater.clear();
      routingTools.clear();
      state.settings = null; state.modelCooldowns = []; state.accounts = []; state.selected.clear();
      state.accountsLoaded = false; state.modelCooldownsDegraded = false;
      state.availableCredits = null; state.consumedCredits = null; state.accountsRefreshedAt = null;
      state.models = []; state.usage = null; state.creditUsage = null; state.requestRecords = null;
      state.modelsLoaded = false; state.creditsLoaded = false;
      state.accountsRefreshPromise = null; state.updateSourceTesting = false; state.updateSourceResults = {};
      requestRecords.clear();
      state.creditUsageBusy = false;
      keyManagement.clear(); connections.clear(); playground.clear(); diagnostics.clear(); operations.clear(); notifications.clear(); reports.clear(); settingsTools.clear(); accountTools.clear(); analytics.clear(); modelPolicies.clear(); admissionControl.clear(); jobManagement.clear(); storageManagement.clear(); clearCacheStatus();
      for (const dialog of document.querySelectorAll("dialog[open]")) dialog.close();
      for (const id of ["redemption-auto-code", "account-password", "redemption-code"]) $("#" + id).value = "";
      for (const id of ["account-rows", "account-cards", "model-rows", "model-cards", "credit-usage-rows", "request-record-rows", "request-record-cards", "model-credit-stats-rows", "model-credit-stats-cards"]) $("#" + id).replaceChildren();
      for (const id of ["model-error", "usage-error", "credit-usage-error", "request-record-error"]) $("#" + id).hidden = true;
      for (const id of ["update-check", "update-install", "update-source-test-all", "update-source-save", "refresh-button", "checkin-all-button", "confirm-delete"]) $("#" + id).disabled = false;
      accountForm.querySelector("[type=submit]").disabled = false;
      $("#refresh-button").textContent = "刷新积分";
      $("#redemption-history-list").replaceChildren(); $("#redeem-targets").replaceChildren();
      window.clearTimeout(state.toastTimer); $("#toast").dataset.visible = "false"; $("#toast").textContent = "";
      analyticsView.renderUsage();
      accounts.render();
      for (const id of ["model-count", "credit-usage-count", "request-record-count"]) { const node = $("#" + id); if (node) node.textContent = "等待加载"; }
    }
    else { void jobManagement.load(); operations.start(); }
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

  const updater = window.createUpdater({ $, state, element, api, loadSettings: () => settingsPanel.load(), showToast });
  const { renderUpdateSourceOptions, updateUpdateSourceResult, checkForUpdates, installUpdate, testUpdateSources, saveUpdateSource } = updater;

  async function api(path, options = {}) {
    return apiClient.request(path, options);
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

  function redemptionHistoryStatus(record) {
    if (record.status === "success") return "成功";
    if (record.status === "skipped") return "跳过";
    return "失败";
  }

  function renderRedemptionHistory(records) {
    const list = $("#redemption-history-list");
    list.replaceChildren();
    const items = Array.isArray(records) ? records : [];
    $("#redemption-history-empty").hidden = items.length > 0;
    for (const record of items) {
      const item = element("li", "redemption-history-item");
      const top = element("div", "redemption-history-top");
      top.append(element("strong", "", record.account || "账号"), element("span", record.status === "success" ? "result-success" : "result-error", redemptionHistoryStatus(record)));
      const details = element("div", "redemption-history-details");
      details.append(element("span", "", formatTime(record.createdAt)), element("code", "", record.code || ""));
      if (record.message) details.append(element("span", "", record.message));
      item.append(top, details);
      list.append(item);
    }
  }

  async function loadRedemptionHistory() {
    const epoch = state.sessionGeneration;
    try {
      const result = await api("/admin/api/redemption-history?limit=50");
      if (epoch !== state.sessionGeneration) return;
      renderRedemptionHistory(result.records);
    } catch (error) {
      if (epoch !== state.sessionGeneration) return;
      if (error.message !== "登录状态已过期，请重新登录") {
        $("#redemption-history-empty").textContent = error.message;
        $("#redemption-history-empty").hidden = false;
      }
    }
  }

  function saveNavigation(replace = false) {
    navigation.write({ view: state.view, page: state.requestRecordsPage, creditPage: state.creditUsagePage, filters: state.requestRecordFilters, accounts: accountTools.getState() }, replace);
  }

  function restoreNavigation() {
    const saved = navigation.read();
    state.requestRecordsPage = saved.page;
    state.creditUsagePage = saved.creditPage;
    state.requestRecordFilters = saved.filters;
    for (const [name, value] of Object.entries(saved.filters)) {
      const input = $("#request-filter-" + name);
      if (input.tagName === "SELECT" && value && !Array.from(input.options).some(option => option.value === value)) input.append(new Option(value, value));
      input.value = value;
    }
    accountTools.setState(saved.accounts);
    selectView(saved.view, false);
    if (state.accounts.length) accounts.render();
  }

  function selectView(view, remember = true) {
    state.view = view;
    if (remember) saveNavigation();
    void loadCacheStatus();
    const analytics = view === "analytics";
    const records = view === "records";
    const settings = view === "settings";
    const keys = view === "keys";
    $("#accounts-view").hidden = analytics || records || settings || keys;
    $("#analytics-view").hidden = !analytics;
    $("#records-view").hidden = !records;
    $("#settings-view").hidden = !settings;
    $("#keys-view").hidden = !keys;
    document.querySelectorAll("[data-view]").forEach(button => {
      const selected = button.dataset.view === view;
      button.setAttribute("aria-selected", String(selected));
      button.tabIndex = selected ? 0 : -1;
      if (selected) $("#current-view-label").textContent = button.lastElementChild.textContent;
    });
    if (analytics) { void loadAnalytics(false, true); void accounts.refresh(false, false, true); void reports.load(); }
    else if (records) void requestRecords.load(false);
    else if (settings) { void settingsPanel.load(); void admissionControl.load(); void storageManagement.load(); void operations.load(); void notifications.load(); }
    else if (keys) void keyManagement.load();
    else void accounts.refresh(false, false, true);
  }

  let cacheStatusTimer = null;
  const cacheNotices = new Map();
  function clearCacheStatus() { window.clearTimeout(cacheStatusTimer); cacheStatusTimer = null; for (const notice of cacheNotices.values()) notice.remove(); cacheNotices.clear(); }
  async function loadCacheStatus() {
    const epoch = state.sessionGeneration;
    window.clearTimeout(cacheStatusTimer); cacheStatusTimer = null;
    try {
      const result = await api("/admin/api/cache-status"); if (epoch !== state.sessionGeneration) return;
      const states = { empty: "暂无缓存", hit: "缓存有效", stale: "缓存已过期", "stale-on-error": "刷新失败，显示历史数据", partial: "部分账号刷新失败" };
      for (const [key, selector] of [["accounts", "#accounts-view .section-heading"], ["models", "[aria-labelledby='models-title'] .section-heading"], ["credits", "[aria-labelledby='credit-usage-title'] .section-heading"]]) {
        const status = result[key]; if (!status) continue;
        let notice = cacheNotices.get(key);
        if (!notice) { notice = element("p", "field-note cache-notice"); notice.dataset.resource = key; const heading = document.querySelector(selector); if (!heading) continue; heading.after(notice); cacheNotices.set(key, notice); }
        notice.textContent = (key === "credits" ? "积分记录：" : key === "models" ? "模型：" : "账号：") + (states[status.cacheState] || status.cacheState) + (status.refreshing ? " · 正在刷新" : "") + (status.cachedAt && !status.cachedAt.startsWith("0001") ? " · " + formatTime(status.cachedAt) : "") + (status.partialFailures?.length ? " · 失败 " + status.partialFailures.length + " 个账号" : "");
      }
      if (Object.values(result).some(item => item.refreshing)) cacheStatusTimer = window.setTimeout(loadCacheStatus, 2000);
    } catch (_) { /* Resource requests retain their own actionable errors. */ }
  }

  async function loadAnalytics(force = false, silent = false) {
    return analytics.load(force);
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
    void settingsPanel.loadRedemption();
    void loadRedemptionHistory();
  }

  async function redeem(event) {
    event.preventDefault();
    const epoch = state.sessionGeneration;
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
      await jobManagement.submit("redeem", ids, code);
    } catch (error) {
      if (epoch === state.sessionGeneration) $("#redeem-form-error").textContent = error.message;
    } finally {
      if (epoch === state.sessionGeneration) { submit.disabled = false; submit.textContent = "开始兑换"; }
    }
  }

  function updateRedemptionProgress(job) {
    if (!job || job.id !== state.redemptionProgressJobId) return;
    const items = Array.isArray(job.items) ? job.items : [];
    const results = items.map(item => {
      let payload = {};
      try { payload = typeof item.result === "string" ? JSON.parse(item.result) : (item.result || {}); } catch (_) {}
      const account = state.accounts.find(candidate => candidate.id === item.target);
      const stateLabel = ({ queued: "等待兑换", running: "正在兑换", succeeded: "兑换成功", failed: "兑换失败", cancelled: "已取消", interrupted: "结果待确认" })[item.state] || "等待兑换";
      const message = payload.message || (item.error === "operation_failed" ? "兑换失败" : item.error || stateLabel);
      return {
        account: payload.account || payload.identifier || account?.identifier || item.target || "账号",
        state: item.state,
        status: item.state === "succeeded" ? (payload.status || "success") : item.state,
        message: item.state === "succeeded" ? message : stateLabel === message ? stateLabel : stateLabel + " · " + message,
        credits: payload.credits,
      };
    });
    const successCount = results.filter(item => item.state === "succeeded").length;
    const finishedCount = results.filter(item => ["succeeded", "failed", "cancelled", "interrupted"].includes(item.state)).length;
    let totalCredits = 0;
    for (const item of results) if (item.state === "succeeded" && Number.isFinite(Number(item.credits))) totalCredits += Number(item.credits);
    const totalCount = results.length;
    const complete = !["queued", "running"].includes(job.state);
    $("#result-title").textContent = !complete ? "兑换处理中" : successCount === totalCount && totalCount > 0 ? "兑换完成" : successCount > 0 ? "兑换部分完成" : "兑换未完成";
    $("#result-close").textContent = complete ? "完成" : "后台继续";
    $("#result-summary").textContent = complete ? "成功 " + formatNumber(successCount) + " / " + formatNumber(totalCount) +
      " 个账号，到账 " + formatNumber(totalCredits) + " 积分。失败账号不会自动重试。" : "已完成 " + formatNumber(finishedCount) + " / " + formatNumber(totalCount) +
      " 个账号，成功 " + formatNumber(successCount) + " 个，到账 " + formatNumber(totalCredits) + " 积分。";
    const list = $("#result-list");
    list.replaceChildren();
    for (const item of results) {
      const row = element("li");
      const name = element("span", "", item.account || "账号");
      const statusClass = item.state === "succeeded" ? "result-success" : ["failed", "cancelled", "interrupted"].includes(item.state) ? "result-error" : "result-pending";
      const details = element("span", statusClass, item.message || item.status);
      row.append(name, details);
      list.append(row);
    }
  }

  async function boot() {
    try {
      const session = await api("/admin/api/session");
      setLogin(session.enabled, session.authenticated, session.setupRequired);
      if (session.authenticated) {
        restoreNavigation();
        if (state.view !== "accounts") void accounts.refresh(false, false, true);
        void checkForUpdates(true);
      }
    } catch (error) {
      setLogin(false, false);
      loginError.textContent = error.message;
    }
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
      restoreNavigation();
      if (state.view !== "accounts") void accounts.refresh(false, false, true);
      void checkForUpdates(true);
    } catch (error) {
      loginError.textContent = error.message;
    } finally {
      button.disabled = false;
    }
  });

  $("#logout-button").addEventListener("click", async () => {
    try { await api("/admin/api/session", { method: "DELETE" }); setLogin(true, false, false); }
    catch (error) { showToast("退出失败：" + error.message); }
  });
  $("#analytics-refresh").addEventListener("click", () => { void loadAnalytics(true); void accounts.refresh(false, true, true); void reports.load(); });
  $("#update-check").addEventListener("click", () => checkForUpdates(false));
  $("#update-install").addEventListener("click", installUpdate);
  $("#update-source-test-all").addEventListener("click", testUpdateSources);
  $("#update-custom-prefix").addEventListener("input", () => {
    const custom = state.updateSourceOptions.find(option => option.id === "custom");
    if (custom) updateUpdateSourceResult(custom, $("#update-custom-prefix").value.trim() ? null : { skipped: true });
  });
  $("#update-source-save").addEventListener("click", () => saveUpdateSource());
  document.querySelectorAll("[data-view]").forEach(button => {
    button.addEventListener("click", () => selectView(button.dataset.view));
    button.addEventListener("keydown", event => {
      if (!["ArrowLeft", "ArrowRight", "ArrowUp", "ArrowDown", "Home", "End"].includes(event.key)) return;
      event.preventDefault();
      const views = Array.from(document.querySelectorAll("[data-view]")).map(item => item.dataset.view);
      const current = views.indexOf(button.dataset.view);
      const index = event.key === "Home" ? 0 : event.key === "End" ? views.length - 1 : (current + (["ArrowRight", "ArrowDown"].includes(event.key) ? 1 : views.length - 1)) % views.length;
      const view = views[index];
      selectView(view);
      $("[data-view='" + view + "']").focus();
    });
  });
  $("#redeem-open").addEventListener("click", openRedeemDialog);
  $("#redeem-history-refresh").addEventListener("click", loadRedemptionHistory);
  redeemForm.addEventListener("submit", redeem);

  document.querySelectorAll("[data-close]").forEach(button => {
    button.addEventListener("click", () => {
      const dialog = document.getElementById(button.dataset.close);
      if (dialog) dialog.close();
    });
  });
  $("#select-enabled").addEventListener("click", () => {
    $("#redeem-targets").querySelectorAll("input[type=checkbox]").forEach(input => { input.checked = true; });
  });

  const modelPolicies = window.createModelPolicies({ api, element, showToast, render: () => analyticsView.renderModels() });
  const analyticsView = window.createAnalyticsView({ $, state, api, element, modelPolicies, accountAvailable: account => accounts.accountState(account).className === "status-ready", openPlayground: id => playground.open(id), formatNumber, formatTime, formatDuration, hasNumber, saveNavigation });
  analyticsView.bind();
  const analytics = window.createAnalytics({ api, element, state, modelPolicies, renderModels: analyticsView.renderModels, renderUsage: analyticsView.renderUsage, renderCredits: analyticsView.renderCredits, creditUsagePath: analyticsView.creditUsagePath, formatTime });
  const admissionControl = window.createAdmission({ api, element });
  const storageManagement = window.createStorage({ api, element, formatTime });
  const jobManagement = window.createJobs({ api, element, formatTime, accountName: id => state.accounts.find(account => account.id === id)?.identifier || id, showToast, onSubmitted: job => {
    if (job.kind !== "redeem") return;
    state.redemptionProgressJobId = job.id;
    redeemDialog.close();
    updateRedemptionProgress(job);
    resultDialog.showModal();
  }, onUpdate: updateRedemptionProgress, onComplete: () => {
    void accounts.refresh(false, true);
    void loadRedemptionHistory();
  } });
  const keyManagement = window.createKeyManagement({ api, element, formatTime, formatNumber, showToast, openRecords: id => {
    state.requestRecordFilters = { model: "", account: "", type: "", status: "", key: id };
    state.requestRecordsPage = 1;
    for (const field of ["model", "account", "type", "status"]) $("#request-filter-" + field).value = "";
    $("#request-filter-key").value = id;
    selectView("records");
  } });
  const connections = window.createConnections({ api, element, showToast });
  const playground = window.createPlayground({ element, onUnauthorized: () => setLogin(true, false, false), onComplete: () => { void accounts.refresh(false, true, true); }, getModels: () => state.models.filter(model => modelPolicies.rule(model.id)?.enabled !== false).map(model => {
    const rule = modelPolicies.rule(model.id);
    return { id: rule?.publicId || model.id, endpoints: rule?.endpoints };
  }) });
  const routingTools = window.createRoutingTools({ element, onChange: strategy => accounts.updateStrategy(strategy) });
  const reports = window.createReports({ api, element, formatTime });
  const notifications = window.createNotifications({ api, element, formatTime });
  const diagnostics = window.createDiagnostics({ api, element, formatTime, formatDuration, showToast });
  const requestRecords = window.createRequestRecords({ api, $, element, state, dashboard, diagnostics, formatNumber, formatTime, formatDuration, hasNumber, saveNavigation });
  requestRecords.bind();
  const operations = window.createOperations({ api, element, formatTime });
  const settingsTools = window.createSettingsTools({ element, onGroup: name => { if (name === "operations" && !dashboard.hidden) void operations.load(); } });
  const settingsPanel = window.createSettings({ $, api, state, element, settingsTools, renderUpdateSourceOptions, showToast });
  settingsPanel.bind();
  const accounts = window.createAccounts({ $, api, state, element, formatNumber, formatTime, formatChinaTime, hasNumber, showToast, routingTools,
    onChange: () => analyticsView.renderModels(),
    visibleAccounts: () => accountTools.visible(), updateTools: () => accountTools.update(), submitJob: (...args) => jobManagement.submit(...args) });
  accounts.bind();
  const accountTools = window.createAccountTools({ api, element, getAccounts: () => state.accounts, getSelected: () => state.selected, accountState: account => accounts.accountState(account),
    submitJob: (kind, ids) => jobManagement.submit(kind, ids),
    onChange: replace => { saveNavigation(replace); accounts.renderList(); },
    onSelection: () => accounts.renderList(),
    onComplete: () => accounts.refresh(false, true), showToast });
  $(".select-all-control span").textContent = "选择当前筛选账号";
  window.addEventListener("popstate", () => { if (!dashboard.hidden) restoreNavigation(); });
  boot();
})();
