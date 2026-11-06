(() => {
  const $ = (selector, root = document) => root.querySelector(selector);
  const state = {
    accounts: [],
    modelCooldowns: [],
    sessionGeneration: 0,
    selected: new Set(),
    selectionReady: false,
    strategy: "fixed",
    deletingID: "",
    toastTimer: 0,
    availableCredits: null,
    consumedCredits: null,
    accountsRefreshedAt: null,
    accountsRefreshPromise: null,
    analyticsRefreshPromise: null,
    analyticsLoaded: false,
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
  const accountDialog = $("#account-dialog");
  const accountForm = $("#account-form");
  const redeemDialog = $("#redeem-dialog");
  const redeemForm = $("#redeem-form");
  const confirmDialog = $("#confirm-dialog");
  const resultDialog = $("#result-dialog");
  const promptDialog = $("#prompt-dialog");
  const requestErrorDialog = $("#request-error-dialog");

  function element(tag, className, text) {
    const node = document.createElement(tag);
    if (className) node.className = className;
    if (text !== undefined) node.textContent = text;
    return node;
  }

  function setLogin(enabled, authenticated, setupRequired = false) {
    if (!authenticated) { state.sessionGeneration++; state.settings = null; state.modelCooldowns = []; keyManagement.clear(); modelPolicies.clear(); admissionControl.clear(); jobManagement.clear(); storageManagement.clear(); clearCacheStatus(); $("#redemption-auto-code").value = ""; }
    else void jobManagement.load();
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

  function renderUpdateStatus(status) {
    const currentVersion = status.currentVersion === "dev" ? "未发布版本" : (status.currentVersion || "未知版本");
    $("#update-current-version").textContent = currentVersion;
    $("#update-platform").textContent = status.platform || "";
    $(".sidebar-version").textContent = "Clodesen " + currentVersion;
    $("#update-notes").textContent = status.releaseNotes || "";
    $("#update-notes").hidden = !status.releaseNotes;
    $("#update-download").href = status.downloadURL || status.releaseURL || "#";
    $("#update-download").hidden = !status.updateAvailable || status.canInstall;
    $("#update-install").hidden = !status.canInstall;
    if (Array.isArray(status.availableSources)) {
      state.updateSourceOptions = status.availableSources;
      const sourceID = state.settings?.update?.source || state.updateSourceID || status.sourceID || "direct";
      renderUpdateSourceOptions(status.availableSources, sourceID);
      $("#update-custom-prefix").value = state.settings?.update?.customPrefix || "";
    }
    if (status.updateAvailable) {
      $("#update-status").textContent = "发现新版本 " + status.latestVersion + (status.canInstall ? "，可直接更新。" : "，请下载对应平台的 ZIP 包。");
    } else {
      $("#update-status").textContent = status.latestVersion ? "当前已是最新版本 " + currentVersion : "未找到可用的发布版本";
    }
  }

  function updateSourceResultText(option, result) {
    if (!result) {
      if (option.id === "custom" && !$("#update-custom-prefix").value.trim()) return "填写地址后可测速";
      return "未测速";
    }
    if (result.pending) return "测速中…";
    if (result.skipped) return "填写地址后可测速";
    if (result.ok) return "可用 · " + result.latencyMs + " ms";
    return "不可用";
  }

  function updateUpdateSourceTrigger() {
    const selected = state.updateSourceOptions.find(option => option.id === state.updateSourceID);
    const label = selected?.name || "直连";
    const triggerLabel = $("#update-source-current");
    triggerLabel.textContent = label;
    triggerLabel.title = label;
  }

  function updateUpdateSourceControls(sourceID) {
    const customSelected = sourceID === "custom";
    $("#update-custom-wrap").hidden = !customSelected;
    $("#update-source-test-all").hidden = customSelected;
    $("#update-source-save").hidden = !customSelected;
  }

  function renderUpdateSourceOptions(options, selectedID) {
    const container = $("#update-source-options");
    const known = options.some(option => option.id === selectedID);
    state.updateSourceID = known ? selectedID : "direct";
    updateUpdateSourceTrigger();
    updateUpdateSourceControls(state.updateSourceID);
    container.replaceChildren(...options.map(option => {
      const row = element("label", "update-source-option");
      row.dataset.sourceId = option.id;
      const radio = element("input");
      radio.type = "radio";
      radio.name = "update-source";
      radio.value = option.id;
      radio.checked = option.id === state.updateSourceID;
      radio.addEventListener("change", () => {
        state.updateSourceID = option.id;
        updateUpdateSourceControls(option.id);
        updateUpdateSourceTrigger();
        if (option.id === "custom") {
          $("#update-custom-wrap").scrollIntoView({ block: "nearest" });
        } else {
          void saveUpdateSource(false);
        }
      });
      const copy = element("span", "update-source-option-copy");
      const name = element("span", "update-source-option-name", option.name);
      const result = element("span", "update-source-option-result", updateSourceResultText(option, state.updateSourceResults[option.id]));
      const saved = state.updateSourceResults[option.id];
      if (saved?.ok) result.dataset.state = "ok";
      if (saved && !saved.ok && !saved.pending && !saved.skipped) result.dataset.state = "error";
      copy.append(name, result);
      row.append(radio, copy);
      return row;
    }));
    $("#update-custom-wrap").hidden = state.updateSourceID !== "custom";
  }

  function updateUpdateSourceResult(option, result) {
    state.updateSourceResults[option.id] = result;
    const row = Array.from($("#update-source-options").querySelectorAll(".update-source-option"))
      .find(item => item.dataset.sourceId === option.id);
    if (!row) return;
    const output = $(".update-source-option-result", row);
    output.textContent = updateSourceResultText(option, result);
    output.dataset.state = result?.ok ? "ok" : result?.pending || result?.skipped ? "" : result ? "error" : "";
    if (result?.error) output.title = result.error;
    else output.removeAttribute("title");
  }

  function updateSourceSelection() {
    return {
      source: state.updateSourceID || state.settings?.update?.source || "direct",
      customPrefix: $("#update-custom-prefix").value.trim(),
    };
  }

  async function saveUpdateSource(closeAfterSave = true) {
    const epoch = state.sessionGeneration;
    const button = $("#update-source-save");
    const sourceRadios = Array.from($("#update-source-options").querySelectorAll("input[type=radio]"));
    button.disabled = true;
    sourceRadios.forEach(radio => { radio.disabled = true; });
    $("#update-source-result").textContent = "";
    try {
      const selection = updateSourceSelection();
      await api("/admin/api/update", { method: "PUT", body: JSON.stringify(selection) });
      if (epoch !== state.sessionGeneration) return;
      state.updateSourceID = selection.source;
      await loadSettings();
      if (epoch !== state.sessionGeneration) return;
      if (closeAfterSave) $("#update-source-disclosure").open = false;
      showToast("更新加速源已保存");
    } catch (error) {
      if (epoch !== state.sessionGeneration) return;
      $("#update-source-result").textContent = error.message;
    } finally {
      if (epoch !== state.sessionGeneration) return;
      button.disabled = false;
      sourceRadios.forEach(radio => { radio.disabled = false; });
    }
  }

  async function testUpdateSources() {
    if (state.updateSourceTesting) return;
    const button = $("#update-source-test-all");
    const customPrefix = $("#update-custom-prefix").value.trim();
    const options = state.updateSourceOptions.filter(option => option.id !== "custom" || customPrefix);
    const custom = state.updateSourceOptions.find(option => option.id === "custom");
    if (custom && !customPrefix) updateUpdateSourceResult(custom, { skipped: true });
    state.updateSourceTesting = true;
    button.disabled = true;
    $("#update-source-save").disabled = true;
    $("#update-source-result").textContent = "正在测速 0/" + options.length;
    options.forEach(option => updateUpdateSourceResult(option, { pending: true }));
    let completed = 0;
    let available = 0;
    await Promise.all(options.map(async option => {
      let result;
      try {
        result = await api("/admin/api/update", {
          method: "PATCH",
          body: JSON.stringify({ source: option.id, customPrefix }),
        });
      } catch (error) {
        result = { ok: false, error: error.message };
      }
      completed += 1;
      if (result.ok) available += 1;
      updateUpdateSourceResult(option, result);
      $("#update-source-result").textContent = "正在测速 " + completed + "/" + options.length;
    }));
    state.updateSourceTesting = false;
    button.disabled = false;
    $("#update-source-save").disabled = false;
    $("#update-source-result").textContent = "测速完成：" + available + "/" + options.length + " 个源可用";
  }

  async function checkForUpdates(silent = false) {
    const button = $("#update-check");
    button.disabled = true;
    if (!silent) $("#update-status").textContent = "正在检查版本";
    try {
      renderUpdateStatus(await api("/admin/api/update"));
    } catch (error) {
      if (error.message === "登录状态已过期，请重新登录") throw error;
      $("#update-status").textContent = "检查更新失败，请稍后重试";
    } finally {
      button.disabled = false;
    }
  }

  async function installUpdate() {
    const button = $("#update-install");
    button.disabled = true;
    $("#update-status").textContent = "正在下载并校验更新包，耗时可能受网络影响";
    try {
      const result = await api("/admin/api/update", { method: "POST", body: "{}" });
      $("#update-status").textContent = result.message || "更新已安装，服务即将重启";
      showToast($("#update-status").textContent);
      window.setTimeout(() => window.location.reload(), 5000);
    } catch (error) {
      $("#update-status").textContent = error.message;
      button.disabled = false;
    }
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
    if (!response.ok) {
      const error = new Error(typeof payload?.error === "string" ? payload.error : payload?.error?.message || "请求失败");
      error.status = response.status;
      throw error;
    }
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
    return "";
  }

  function modelPricingParameter(model) {
    const parts = [];
    if (typeof model.image_resolution === "string" && model.image_resolution.trim()) {
      parts.push("分辨率 " + model.image_resolution.trim());
    }
    if (typeof model.upstream_model === "string" && model.upstream_model.trim()) {
      parts.push("基于 " + model.upstream_model.trim());
    }
    const pricing = model.tokenPricing;
    if (pricing && typeof pricing === "object" && !Array.isArray(pricing)) {
      const labels = { maxOutputTokens: "最大输出", mode: "计费方式", input: "输入", output: "输出", cacheRead: "缓存读取", cacheWrite: "缓存写入", contextAbove: "超出上下文" };
      for (const [key, value] of Object.entries(pricing)) {
        if (value === null || value === undefined || typeof value === "object") continue;
        const label = labels[key] || String(key).replaceAll("_", " ");
        parts.push(label + " " + (typeof value === "number" ? formatNumber(value) : String(value)));
        if (parts.length >= 3) break;
      }
    }
    return parts.join(" · ");
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
    if (record.type === "credit_expired") return "过期 " + creditAmount(record.amount) + " 积分";
    return creditDirection(record) + " " + creditAmount(record.amount) + " 积分";
  }

  function creditRecordStatus(record) {
    if (record.type === "refund") return "已退回";
    if (record.type === "credit_expired") return "已过期";
    if (record.status === "completed") return "已完成";
    if (record.status === "refunded") return "已退款";
    return record.status || "--";
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
    try {
      const result = await api("/admin/api/redemption-history?limit=50");
      renderRedemptionHistory(result.records);
    } catch (error) {
      if (error.message !== "登录状态已过期，请重新登录") {
        $("#redemption-history-empty").textContent = error.message;
        $("#redemption-history-empty").hidden = false;
      }
    }
  }

  function selectView(view) {
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
    });
    if (analytics) void loadAnalytics(false, true);
    else if (records) void loadRequestRecords(false);
    else if (settings) { void loadSettings(); void admissionControl.load(); void storageManagement.load(); }
    else if (keys) void keyManagement.load();
    else void refresh(false, false, true);
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

  function setSettingsForm(settings) {
    state.settings = settings;
    $("#settings-server-addr").value = settings.server?.addr || "";
    $("#settings-base-url").value = settings.upstream?.baseURL || "";
    $("#settings-openai-url").value = settings.upstream?.openAIBaseURL || "";
    $("#settings-login-path").value = settings.upstream?.loginPath || "";
    $("#settings-login-timeout").value = settings.upstream?.loginTimeout || "30s";
    $("#settings-request-timeout").value = settings.upstream?.requestTimeout || "0s";
    $("#settings-user-agent").value = settings.upstream?.userAgent || "";
    $("#settings-strategy").value = settings.auth?.strategy || "fixed";
    $("#settings-account-rpm").value = settings.auth?.rpm || 4;
    $("#settings-auto-relogin").checked = Boolean(settings.auth?.autoRelogin);
    $("#settings-checkin-enable").checked = Boolean(settings.checkin?.enable);
    $("#settings-checkin-path").value = settings.checkin?.path || "";
    $("#settings-checkin-at").value = settings.checkin?.at || "04:00";
    $("#settings-checkin-timezone").value = settings.checkin?.timezone || "Asia/Shanghai";
    $("#update-custom-prefix").value = settings.update?.customPrefix || "";
    state.updateSourceID = settings.update?.source || "builtin:gh-proxy-com";
    if (state.updateSourceOptions.length) renderUpdateSourceOptions(state.updateSourceOptions, state.updateSourceID);
    else $("#update-custom-wrap").hidden = state.updateSourceID !== "custom";
    setRedemptionSettingsForm(settings);
    renderSettingsSources(settings);
  }

  function renderSettingsSources(settings) {
    const controls = {
      "server.addr": "settings-server-addr", "upstream.baseURL": "settings-base-url", "upstream.openAIBaseURL": "settings-openai-url",
      "upstream.loginPath": "settings-login-path", "upstream.loginTimeout": "settings-login-timeout", "upstream.requestTimeout": "settings-request-timeout", "upstream.userAgent": "settings-user-agent",
      "auth.strategy": "settings-strategy", "auth.rpm": "settings-account-rpm", "auth.autoRelogin": "settings-auto-relogin",
      "checkin.enable": "settings-checkin-enable", "checkin.path": "settings-checkin-path", "checkin.at": "settings-checkin-at", "checkin.timezone": "settings-checkin-timezone",
      "redemption.enable": "redemption-auto-enable", "redemption.at": "redemption-auto-at", "redemption.code": "redemption-auto-code",
    };
    const sources = { cli: "命令行", environment: "环境变量", management: "管理设置", file: "配置文件", default: "默认值" };
    for (const [path, id] of Object.entries(controls)) {
      const input = document.getElementById(id); const field = settings.fields?.[path];
      if (!input) continue;
      input.disabled = Boolean(field?.locked);
      const parent = input.closest("label"); if (!parent) continue;
      parent.querySelector(".setting-source")?.remove();
      if (field) parent.append(element("span", "field-note setting-source", sources[field.source] + (field.locked ? " · 已锁定" : field.pending ? " · 待重启" : field.mode === "restart" ? " · 重启生效" : " · 即时生效")));
    }
  }

  function setRedemptionSettingsForm(settings) {
    state.settings = settings;
    $("#redemption-auto-enable").checked = Boolean(settings?.redemption?.enable);
    $("#redemption-auto-at").value = settings?.redemption?.at || "04:30";
    $("#redemption-auto-code").value = settings?.redemption?.code || "";
    $("#redemption-auto-note").textContent = settings?.redemption?.codeConfigured ? "自动兑换码已配置。" : "尚未配置兑换码。";
    $("#redemption-auto-error").textContent = "";
  }

  async function loadSettings() {
    const epoch = state.sessionGeneration;
    try { const settings = await api("/admin/api/config"); if (epoch !== state.sessionGeneration) return; setSettingsForm(settings); $("#settings-error").textContent = ""; }
    catch (error) { if (epoch === state.sessionGeneration) $("#settings-error").textContent = error.message; }
  }

  async function saveSettings(event) {
    event.preventDefault();
    const button = $("#settings-save");
    button.disabled = true;
    $("#settings-error").textContent = "";
    const epoch = state.sessionGeneration;
    const payload = { ...state.settings, server: { addr: $("#settings-server-addr").value.trim() }, upstream: {
      baseURL: $("#settings-base-url").value.trim(), openAIBaseURL: $("#settings-openai-url").value.trim(),
      loginPath: $("#settings-login-path").value.trim(), loginTimeout: $("#settings-login-timeout").value.trim(),
      requestTimeout: $("#settings-request-timeout").value.trim(), userAgent: $("#settings-user-agent").value.trim(),
    }, auth: { strategy: $("#settings-strategy").value, rpm: Number($("#settings-account-rpm").value), autoRelogin: $("#settings-auto-relogin").checked }, checkin: {
      enable: $("#settings-checkin-enable").checked, path: $("#settings-checkin-path").value.trim(),
      at: $("#settings-checkin-at").value, timezone: $("#settings-checkin-timezone").value.trim(),
    }, redemption: {
      enable: Boolean(state.settings?.redemption?.enable), at: state.settings?.redemption?.at || "04:30",
      code: state.settings?.redemption?.code || "",
    }};
    try { const result = await api("/admin/api/config", { method: "PUT", body: JSON.stringify(payload) }); if (epoch !== state.sessionGeneration) return; setSettingsForm(result.settings); showToast(result.restartRequired ? "设置已保存，部分设置需重启生效" : "设置已保存"); }
    catch (error) { if (epoch === state.sessionGeneration) { if (error.status === 409) await loadSettings(); $("#settings-error").textContent = error.message; } }
    finally { if (epoch === state.sessionGeneration) button.disabled = false; }
  }

  async function loadRedemptionSettings() {
    const epoch = state.sessionGeneration;
    try {
      const settings = await api("/admin/api/config");
      if (epoch !== state.sessionGeneration) return;
      setRedemptionSettingsForm(settings);
      renderSettingsSources(settings);
    } catch (error) {
      if (epoch === state.sessionGeneration && error.message !== "登录状态已过期，请重新登录") $("#redemption-auto-error").textContent = error.message;
    }
  }

  async function saveRedemptionSettings() {
    const epoch = state.sessionGeneration;
    const button = $("#redemption-settings-save");
    button.disabled = true;
    $("#redemption-auto-error").textContent = "";
    try {
      const current = state.settings || await api("/admin/api/config");
      const payload = JSON.parse(JSON.stringify(current));
      payload.redemption = {
        enable: $("#redemption-auto-enable").checked,
        at: $("#redemption-auto-at").value,
        code: $("#redemption-auto-code").value.trim(),
      };
      const result = await api("/admin/api/config", { method: "PUT", body: JSON.stringify(payload) });
      if (epoch !== state.sessionGeneration) return;
      setRedemptionSettingsForm(result.settings);
      renderSettingsSources(result.settings);
      showToast(result.restartRequired ? "自动兑换设置已保存，重启后完整生效" : "自动兑换设置已保存");
    } catch (error) {
      if (epoch === state.sessionGeneration) $("#redemption-auto-error").textContent = error.message;
    } finally {
      if (epoch === state.sessionGeneration) button.disabled = false;
    }
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
    const pricedModels = models.some(model => modelCredits(model) || modelPricingParameter(model));
    $("#model-credits-heading").hidden = !pricedModels;
    for (const model of models) {
      const id = model.id || model.name || "未命名模型";
      const credits = modelCredits(model);
      const parameter = modelPricingParameter(model);
      const row = element("tr");
      const identity = element("td", "model-id");
      const policy = modelPolicies.rule(id);
      identity.append(element("strong", "", policy?.publicId || id));
      if (policy && (policy.publicId !== id || policy.upstreamId !== id)) identity.append(element("div", "field-note", id + " → " + policy.upstreamId));
      row.append(identity);
      if (pricedModels) {
        const creditCell = element("td", "model-credits");
        if (credits) creditCell.append(element("span", "model-credit-value", credits));
        if (parameter) creditCell.append(element("span", "model-credit-parameter", parameter));
        row.append(creditCell);
      }
      const actions = element("td"); actions.append(modelPolicies.controls(id)); row.append(actions);
      rows.append(row);

      const card = element("article", "model-card");
      card.append(element("strong", "model-card-id", policy?.publicId || id));
      if (policy && (policy.publicId !== id || policy.upstreamId !== id)) card.append(element("div", "field-note", id + " → " + policy.upstreamId));
      const details = element("div", "model-card-details");
      if (credits) details.append(element("span", "model-credits", credits));
      if (parameter) details.append(element("span", "model-credit-parameter", parameter));
      card.append(details);
      card.append(modelPolicies.controls(id));
      cards.append(card);
    }
  }

  function renderCreditUsage() {
    const result = state.creditUsage;
    const rows = $("#credit-usage-rows");
    rows.replaceChildren();
    const records = result && Array.isArray(result.records) ? result.records : [];
    const total = result && hasNumber(result.total) ? Number(result.total) : records.length;
    const netCredits = result && result.totals && hasNumber(result.totals.netCredits) ? " · 净消耗 " + formatNumber(result.totals.netCredits) + " 积分" : "";
    $("#credit-usage-count").textContent = result ? formatNumber(total) + " 条记录" + netCredits : "等待加载";
    $("#credit-usage-empty").hidden = records.length > 0 || !state.analyticsLoaded;
    $("#credit-usage-table-wrap").hidden = records.length === 0;
    renderCreditPagination(result);
    for (const record of records) {
      const amount = hasNumber(record.amount) ? Number(record.amount) : 0;
      const amountClass = amount > 0 ? "credit-positive" : amount < 0 ? "credit-negative" : "credit-neutral";
      const row = element("tr");
      const details = element("td", "credit-record-meta");
      details.dataset.type = record.type || "unknown";
      details.dataset.status = record.status || "unknown";
      details.append(
        element("span", "credit-meta-badge credit-meta-balance", "余额 " + (hasNumber(record.balanceAfter) ? formatNumber(record.balanceAfter) : "--")),
        element("span", "credit-meta-badge credit-meta-status", "状态 " + creditRecordStatus(record)),
      );
      const model = element("td", "credit-record-model model-id");
      model.append(element("span", "credit-model-badge", record.model || "未标注"));
      const amountCell = element("td", "credit-record-amount");
      amountCell.append(element("span", "credit-amount-badge " + amountClass, creditAmountText(record)));
      row.append(element("td", "credit-record-time", formatTime(record.createdAt)),
        element("td", "credit-record-account", record.account || "--"),
        model, amountCell, details);
      rows.append(row);
    }
  }

  function renderCreditModelStats() {
    const stats = state.creditUsage && Array.isArray(state.creditUsage.modelStats) ? state.creditUsage.modelStats : [];
    const rows = $("#model-credit-stats-rows");
    const cards = $("#model-credit-stats-cards");
    rows.replaceChildren(); cards.replaceChildren();
    $("#model-credit-stats-empty").hidden = stats.length > 0 || !state.analyticsLoaded;
    $("#model-credit-stats-table-wrap").hidden = stats.length === 0;
    $("#model-credit-stats-cards").hidden = stats.length === 0;
    for (const item of stats) {
      const row = element("tr");
      row.append(element("td", "model-id", item.model || "--"), element("td", "", formatNumber(item.requests)), element("td", "", formatNumber(item.creditsUsed) + " 积分"), element("td", "credit-positive", formatNumber(item.creditsRefunded) + " 积分"), element("td", "", formatNumber(item.netCredits) + " 积分"), element("td", "", formatTime(item.lastUsedAt)));
      rows.append(row);
      const card = element("article", "usage-card");
      card.append(element("div", "usage-card-top", item.model || "--"), element("div", "usage-card-counts", "请求 " + formatNumber(item.requests) + " · 扣除 " + formatNumber(item.creditsUsed) + " · 退回 " + formatNumber(item.creditsRefunded) + " · 净消耗 " + formatNumber(item.netCredits)), element("div", "usage-card-details", "最近 " + formatTime(item.lastUsedAt)));
      cards.append(card);
    }
  }

  function renderCreditPagination(result) {
    const pagination = $("#credit-usage-pagination");
    const totalPages = result && hasNumber(result.totalPages) ? Number(result.totalPages) : 1;
    const page = result && hasNumber(result.page) ? Number(result.page) : state.creditUsagePage;
    state.creditUsagePage = page;
    pagination.hidden = !result || totalPages <= 1;
    $("#credit-page-label").textContent = "第 " + page + " / " + totalPages + " 页";
    $("#credit-prev-page").disabled = page <= 1;
    $("#credit-next-page").disabled = page >= totalPages;
  }

  function renderUsage() {
    const usage = state.usage;
    if (!usage) {
      return;
    }
    $("#usage-total").textContent = formatNumber(usage.totalRequests);
    $("#usage-successes").textContent = formatNumber(usage.successes);
    $("#usage-failures").textContent = formatNumber(usage.failures);
    $("#usage-success-rate").textContent = hasNumber(usage.totalRequests) && Number(usage.totalRequests) > 0 ?
      formatPercent(Number(usage.successes || 0) / Number(usage.totalRequests)) : "--";
    $("#usage-latency").textContent = formatDuration(usage.averageLatencyMs);
    $("#usage-period").textContent = usage.startedAt ? "累计统计 · 起始于 " + formatTime(usage.startedAt) : "累计统计";
  }

  function requestTypeLabel(type) {
    return ({ stream: "流式", non_stream: "非流式", image: "生图" })[type] || type || "未知";
  }

  function requestParametersText(parameters) {
    if (!parameters || typeof parameters !== "object" || Object.keys(parameters).length === 0) return "--";
    return Object.entries(parameters).map(([key, value]) => {
      let rendered = "";
      try { rendered = typeof value === "string" ? value : JSON.stringify(value); } catch (_) { rendered = String(value); }
      if (rendered.length > 100) rendered = rendered.slice(0, 97) + "...";
      return key + "=" + rendered;
    }).join(" · ");
  }

  function requestRecordStatus(record) {
	if (record.stage === "started") return "进行中";
	if (record.stage === "interrupted") return "已中断";
    const category = record.statusCategory || (record.success ? "normal" : "abnormal");
    if (category === "normal") return "正常";
    if (category === "upstream_error") return "报错" + (record.status ? " · " + record.status : "");
    return "异常" + (record.status ? " · " + record.status : "");
  }

  function requestStatusNode(record) {
    const label = requestRecordStatus(record);
    const category = record.statusCategory || (record.success ? "normal" : "abnormal");
    if (category === "upstream_error" && record.error) {
      const button = element("button", "prompt-button request-error-button", "查看报错");
      button.type = "button";
      button.addEventListener("click", () => {
        $("#request-error-text").textContent = record.error;
        requestErrorDialog.showModal();
      });
      return button;
    }
    return element("span", "record-status-" + category, label);
  }

  function openPromptDialog(record) {
    $("#request-prompt-text").textContent = record.prompt || "这条记录没有保存提示词。";
    promptDialog.showModal();
  }

  function promptAction(record) {
    const button = element("button", "prompt-button", record.prompt ? "查看提示词" : "无提示词");
    button.type = "button";
    button.disabled = !record.prompt;
    if (record.prompt) button.addEventListener("click", () => openPromptDialog(record));
    return button;
  }

  function attemptDetails(record) {
    if (!Array.isArray(record.attempts) || !record.attempts.length) return null;
    const details = element("details", "request-attempts");
    details.append(element("summary", "", record.attempts.length + " 次上游尝试"));
    const list = element("ol");
    for (const attempt of record.attempts) list.append(element("li", "", [attempt.id, attempt.account || attempt.accountId, attempt.model, attempt.status || "未响应", formatDuration(attempt.durationMs), attempt.errorCode].filter(Boolean).join(" · ")));
    details.append(list);
    return details;
  }

  function renderRequestRecords() {
    const result = state.requestRecords;
    const records = result && Array.isArray(result.records) ? result.records : [];
    const summary = result?.summary || {};
    let storageNotice = $("#audit-storage-health");
    if (!storageNotice) {
      storageNotice = element("p", "inline-error"); storageNotice.id = "audit-storage-health";
      storageNotice.setAttribute("role", "status"); $("#request-record-filters").after(storageNotice);
    }
    storageNotice.hidden = !result?.storage?.degraded;
    storageNotice.textContent = result?.storage?.degraded ? "审计存储异常 · 失败 " + formatNumber(result.storage.failures) + " 次 · 丢弃 " + formatNumber(result.storage.dropped) + " 条 · 最近失败 " + formatTime(result.storage.lastFailure) : "";
    $("#request-record-total").textContent = formatNumber(summary.total ?? result?.total);
    $("#request-record-successes").textContent = formatNumber(summary.successes);
    $("#request-record-failures").textContent = formatNumber(summary.failures);
    $("#request-record-duration").textContent = formatDuration(summary.averageDurationMs);
    $("#request-record-first-token").textContent = formatDuration(summary.averageFirstTokenMs);
    $("#request-record-updated").textContent = result?.refreshedAt ? "最近更新 " + formatTime(result.refreshedAt) : "最近更新 --";
    $("#request-record-count").textContent = result ? formatNumber(result.total) + " 条记录 · 保留最近 7 天" : "最近 7 天";

    const rows = $("#request-record-rows");
    const cards = $("#request-record-cards");
    const accountFilter = $("#request-filter-account");
    const selectedAccount = state.requestRecordFilters.account || accountFilter.value;
    const accountOptions = [element("option", "", "全部账号")];
    accountOptions[0].value = "";
    for (const account of result?.availableAccounts || []) {
      const option = element("option", "", account.label || account.id);
      option.value = account.id;
      accountOptions.push(option);
    }
    accountFilter.replaceChildren(...accountOptions);
    accountFilter.value = selectedAccount;
    rows.replaceChildren();
    cards.replaceChildren();
    $("#request-record-empty").hidden = records.length > 0 || !result;
    $("#request-record-table-wrap").hidden = records.length === 0;
    $("#request-record-cards").hidden = records.length === 0;
    renderRequestRecordPagination(result);
    for (const record of records) {
      const params = requestParametersText(record.parameters);
      const row = element("tr");
      const firstCell = element("td", "request-record-first-cell");
      if (record.type === "image") firstCell.append(promptAction(record));
      else firstCell.textContent = record.firstTokenMs === undefined || record.firstTokenMs === null ? "--" : formatDuration(record.firstTokenMs);
      const thinkingCell = element("td", "request-record-thinking");
      if (record.type !== "image") thinkingCell.textContent = record.thinkingLevel || "未指定";
      const statusCell = element("td", "");
      statusCell.append(requestStatusNode(record));
      const client = element("td", "request-record-client");
      client.append(element("strong", "", record.apiKeyName || record.apiKeyId || "--"), element("small", "muted", record.clientIP || "--"));
      client.title = [record.requestId, record.authErrorCode].filter(Boolean).join(" · ");
      const attempts = attemptDetails(record);
      if (attempts) client.append(attempts);
      row.append(element("td", "request-record-time", formatTime(record.createdAt)),
        element("td", "model-id request-record-model", record.model || "未指定"),
        element("td", "request-record-type " + (record.type || ""), requestTypeLabel(record.type)),
        element("td", "request-record-endpoint", record.endpoint || "--"),
        element("td", "request-record-account", record.account || record.accountId || "--"),
        element("td", "request-record-duration-cell", formatDuration(record.durationMs)),
        firstCell,
        thinkingCell,
        statusCell,
        element("td", "request-record-parameters", params), client);
      rows.append(row);

      const card = element("article", "request-record-card");
      const top = element("div", "request-record-card-top");
      top.append(element("strong", "model-id", record.model || "未指定"), requestStatusNode(record));
      const details = element("div", "request-record-card-grid");
      const fields = [["时间", formatTime(record.createdAt)], ["类型", requestTypeLabel(record.type)], ["端点", record.endpoint || "--"], ["账号", record.account || record.accountId || "--"], ["耗时", formatDuration(record.durationMs)]];
      if (record.type === "image") fields.push(["提示词", promptAction(record)]);
      else fields.push(["首字", record.firstTokenMs == null ? "--" : formatDuration(record.firstTokenMs)], ["思考", record.thinkingLevel || "未指定"]);
      fields.push(["密钥", record.apiKeyName || record.apiKeyId || "--"], ["客户端 IP", record.clientIP || "--"], ["认证", record.authErrorCode || record.authResult || "--"], ["请求 ID", record.requestId || "--"], ["参数", params]);
      for (const [label, value] of fields) {
        const item = element("div", "request-record-card-field");
        item.append(element("span", "field-label", label), value instanceof Node ? value : element("strong", "", value));
        details.append(item);
      }
      card.append(top, details);
      const cardAttempts = attemptDetails(record);
      if (cardAttempts) card.append(cardAttempts);
      cards.append(card);
    }
  }

  function requestRecordsPath() {
    const params = new URLSearchParams({ page: String(state.requestRecordsPage), pageSize: String(state.requestRecordsPageSize) });
    for (const [key, value] of Object.entries(state.requestRecordFilters)) {
      if (value) params.set(key, value);
    }
    return "/admin/api/request-records?" + params.toString();
  }

  function renderRequestRecordPagination(result) {
    const pagination = $("#request-record-pagination");
    const totalPages = result && hasNumber(result.totalPages) ? Number(result.totalPages) : 1;
    const page = result && hasNumber(result.page) ? Number(result.page) : state.requestRecordsPage;
    state.requestRecordsPage = page;
    pagination.hidden = !result || totalPages <= 1;
    $("#request-page-label").textContent = "第 " + page + " / " + totalPages + " 页";
    $("#request-prev-page").disabled = page <= 1;
    $("#request-next-page").disabled = page >= totalPages;
  }

  async function loadRequestRecords(force = false) {
    if (state.requestRecordsController) state.requestRecordsController.abort();
    const controller = new AbortController();
    const requestID = ++state.requestRecordsRequestID;
    state.requestRecordsController = controller;
    const button = $("#records-refresh");
    const oldLabel = button.textContent;
    button.disabled = true;
    $("#request-record-error").hidden = true;
    try {
      const result = await api(requestRecordsPath() + (force ? "&refresh=1" : ""), { signal: controller.signal });
      if (requestID !== state.requestRecordsRequestID) return;
      state.requestRecords = result;
      renderRequestRecords();
    } catch (error) {
      if (error.name === "AbortError" || requestID !== state.requestRecordsRequestID) return;
      if (error.message === "登录状态已过期，请重新登录") throw error;
      $("#request-record-error").textContent = error.message;
      $("#request-record-error").hidden = false;
    } finally {
      if (requestID === state.requestRecordsRequestID) {
        state.requestRecordsController = null;
        button.disabled = false;
        button.textContent = oldLabel;
      }
    }
  }

  function refreshVisibleRequestRecords() {
    if (dashboard.hidden || document.hidden || $("#records-view").hidden) return;
    const now = Date.now();
    if (now - state.requestRecordsLastRefreshAt < 1500) return;
    state.requestRecordsLastRefreshAt = now;
    void loadRequestRecords(true);
  }

  function creditUsagePath(force = false) {
    const params = new URLSearchParams({
      page: String(state.creditUsagePage),
      pageSize: String(state.creditUsagePageSize),
    });
    if (force) params.set("refresh", "1");
    return "/admin/api/credit-usage?" + params.toString();
  }

  async function loadCreditUsage(force = false) {
    if (state.creditUsageBusy) return;
    state.creditUsageBusy = true;
    $("#credit-usage-error").hidden = true;
    try {
      state.creditUsage = await api(creditUsagePath(force));
      renderCreditModelStats();
      renderCreditUsage();
    } catch (error) {
      if (error.message === "登录状态已过期，请重新登录") throw error;
      $("#credit-usage-error").textContent = error.message;
      $("#credit-usage-error").hidden = false;
    } finally {
      state.creditUsageBusy = false;
    }
  }

  async function loadAnalytics(force = false, silent = false) {
    const generation = state.sessionGeneration;
    const existing = state.analyticsRefreshPromise;
    if (existing) {
      if (!force) return existing;
      await existing.catch(() => {});
    }
    if (state.analyticsLoaded && !force && !silent) return;

    const task = (async () => {
      const button = $("#analytics-refresh");
      const oldLabel = button.textContent;
      if (!silent) {
        button.disabled = true;
        button.textContent = "加载中";
        $("#model-error").hidden = true;
        $("#credit-usage-error").hidden = true;
      }
      try {
        const results = await Promise.allSettled([
          api("/admin/api/models" + (force ? "?refresh=1" : "")),
          api("/admin/api/usage"),
          api(creditUsagePath(force)),
        ]);
        if (generation !== state.sessionGeneration) return;
        const modelResult = results[0];
        const usageResult = results[1];
        const creditUsageResult = results[2];
        const failed = results.find(result => result.status === "rejected");
        if (failed) throw failed.reason;
        const nextModels = Array.isArray(modelResult.value.data) ? modelResult.value.data : [];
        const nextUsage = usageResult.value;
        const nextCreditUsage = creditUsageResult.value;
        state.models = nextModels;
        state.usage = nextUsage;
        state.creditUsage = nextCreditUsage;
        state.analyticsLoaded = true;
        await modelPolicies.load();
        if (generation !== state.sessionGeneration) return;
        $("#model-error").hidden = true;
        $("#credit-usage-error").hidden = true;
        renderModels();
        renderUsage();
        renderCreditModelStats();
        renderCreditUsage();
      } catch (error) {
        if (!silent && error.message !== "登录状态已过期，请重新登录") {
          $("#model-error").textContent = error.message;
          $("#model-error").hidden = false;
          showToast(error.message);
        }
      } finally {
        if (!silent) {
          button.disabled = false;
          button.textContent = oldLabel;
        }
      }
    })();
    state.analyticsRefreshPromise = task;
    try {
      return await task;
    } finally {
      if (state.analyticsRefreshPromise === task) state.analyticsRefreshPromise = null;
    }
  }

  function accountState(account) {
    const cooldownUntil = account.cooldownUntil ? new Date(account.cooldownUntil) : null;
    if (!account.enabled) return { text: "已停用", className: "status-paused" };
    if (cooldownUntil && cooldownUntil > new Date()) return { text: cooldownLabel(account) + "，冷却至 " + formatTime(account.cooldownUntil), className: "status-cooldown" };
    if (account.error) return { text: "状态异常", className: "status-error" };
    return { text: "可用", className: "status-ready" };
  }

  function render() {
    const enabled = state.accounts.filter(account => account.enabled);
    const cooling = enabled.filter(account => account.cooldownUntil && new Date(account.cooldownUntil) > new Date());
    const missingBalance = state.accounts.some(account => account.enabled && !hasNumber(account.balance));
    const totalNode = $("#available-credits");
    totalNode.textContent = state.availableCredits === null ? "--" : formatNumber(state.availableCredits);
    $("#consumed-credits").textContent = state.consumedCredits === null ? "--" : formatNumber(state.consumedCredits);
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

  function cooldownLabel(account) {
    return account.cooldownReason === "temporary_ban" ? "临时封禁" : "积分不足";
  }

  function accountDetail(account) {
    const cooldownUntil = account.cooldownUntil ? new Date(account.cooldownUntil) : null;
    if (cooldownUntil && cooldownUntil > new Date()) return cooldownLabel(account) + "，冷却至 " + formatTime(account.cooldownUntil);
    if (account.error) return account.error;
    if (account.redemptionResetAt || account.resetAt) return "兑换码额度重置 " + formatChinaTime(account.redemptionResetAt || account.resetAt);
    return "更新于 " + formatTime(account.refreshedAt);
  }

  function statusBadge(account) {
    const status = accountState(account);
    const badge = element("span", "status-badge " + status.className, status.text);
    return badge;
  }

  function statusWithRPM(account) {
    const stack = element("div", "account-status-stack");
    stack.append(statusBadge(account));
    stack.append(element("span", "account-rpm", "并发 " + (account.rpmUsed ?? 0) + "/" + (account.rpmLimit || 4)));
    return stack;
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

  function appendModelCooldowns(container, account) {
    for (const row of state.modelCooldowns) {
      if (row.accountId !== account.id || new Date(row.until).getTime() <= Date.now()) continue;
      container.append(element("span", "field-note", row.model + " · 上游限流 · 恢复 " + formatTime(row.until)));
    }
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
      appendModelCooldowns(identity, account);
      identityCell.append(identity);
      const balanceCell = element("td", "balance-cell");
      balanceCell.append(element("strong", "", formatNumber(account.balance)));
      const quotaCell = element("td", "quota-cell", quotaText(account));
      const stateCell = element("td");
      stateCell.append(statusWithRPM(account));
      const checkinCell = element("td", "checkin-cell", checkinText(account));
      if (account.checkedIn === true) checkinCell.classList.add("checkin-done");
      if (account.checkedIn === false) checkinCell.classList.add("checkin-pending");
      const actionsCell = element("td", "actions-col");
      const actions = element("div", "row-actions");
      actions.append(actionButton("签到", "checkin"), actionButton("刷新", "refresh"));
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
      appendModelCooldowns(info, account);
      top.append(info);
      top.append(statusWithRPM(account));
      const data = element("div", "account-card-data");
      const balance = element("div");
      balance.append(element("label", "", "积分余额"));
      balance.append(element("strong", "", formatNumber(account.balance)));
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
      actions.append(actionButton("签到", "checkin"), actionButton("刷新", "refresh"));
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

  async function refresh(showLoading = true, force = false, silent = false) {
    const generation = state.sessionGeneration;
    const existing = state.accountsRefreshPromise;
    if (existing) {
      if (!force) return existing;
      await existing.catch(() => {});
    }

    const task = (async () => {
      const button = $("#refresh-button");
      const oldLabel = button.textContent;
      if (!silent) {
        button.disabled = true;
        button.textContent = "刷新中";
      }
      if (!silent && showLoading && !state.accounts.length) {
        $("#loading-state").hidden = false;
        $("#account-table-wrap").hidden = true;
        $("#account-cards").hidden = true;
      }
      if (!silent) $("#error-state").hidden = true;
      try {
        const [result, cooldowns] = await Promise.all([
          api("/admin/api/accounts" + (force ? "?refresh=1" : "")),
          api("/admin/api/model-cooldowns").catch(() => ({ degraded: true, entries: [] })),
        ]);
        if (generation !== state.sessionGeneration) return;
        state.modelCooldowns = Array.isArray(cooldowns.entries) ? cooldowns.entries : [];
        let health = $("#model-cooldown-health");
        if (!health) { health = element("p", "inline-error"); health.id = "model-cooldown-health"; health.setAttribute("role", "status"); $("#account-table-wrap").before(health); }
        health.hidden = !cooldowns.degraded;
        health.textContent = cooldowns.degraded ? "模型冷却存储异常，当前使用本机保护状态" : "";
        const nextAccounts = Array.isArray(result.accounts) ? result.accounts : [];
        state.accounts = nextAccounts;
        state.strategy = result.strategy || "fixed";
        state.availableCredits = hasNumber(result.availableCredits) ? Number(result.availableCredits) : null;
        state.consumedCredits = hasNumber(result.consumedCredits) ? Number(result.consumedCredits) : null;
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
        if (!silent && error.message !== "登录状态已过期，请重新登录") {
          $("#error-state").textContent = error.message;
          $("#error-state").hidden = false;
        }
        if (!silent) {
          $("#service-state").dataset.state = "offline";
          $("#service-state span").textContent = "连接异常";
        }
      } finally {
        if (!silent) {
          button.disabled = false;
          button.textContent = oldLabel;
        }
      }
    })();
    state.accountsRefreshPromise = task;
    try {
      return await task;
    } finally {
      if (state.accountsRefreshPromise === task) state.accountsRefreshPromise = null;
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
    void loadRedemptionSettings();
    void loadRedemptionHistory();
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
      await refresh(false, true);
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
      await refresh(false, true);
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
      await refresh(false, true);
    } catch (error) {
      $("#confirm-copy").textContent = error.message;
    } finally {
      button.disabled = false;
    }
  }

  async function manualCheckin(account = null, button = null) {
    if (button) button.disabled = true;
    if (!account) $("#checkin-all-button").disabled = true;
    try {
      await jobManagement.submit("checkin", account ? [account.id] : []);
    } catch (error) {
      showToast(error.message);
    } finally {
      if (button) button.disabled = false;
      if (!account) $("#checkin-all-button").disabled = false;
    }
  }

  async function refreshAccountStatus(account, button = null) {
    if (button) button.disabled = true;
    try {
      await jobManagement.submit("refresh", [account.id]);
    } catch (error) {
      showToast(error.message);
    } finally {
      if (button) button.disabled = false;
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
      showToast(({ round_robin: "已切换为轮询选择", random: "已切换为随机选择", fixed: "已切换为固定选择" })[strategy] || "账号策略已更新");
    } catch (error) {
      showToast(error.message);
    } finally {
      buttons.forEach(button => { button.disabled = false; });
    }
  }

  function openDeleteDialog(account) {
    state.deletingID = account.id;
    $("#confirm-copy").textContent = "确定移除 " + account.identifier + "？该账号将立即从账号列表中删除。";
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
      await jobManagement.submit("redeem", ids, code);
      redeemDialog.close();
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
    if (button.dataset.action === "checkin") manualCheckin(account, button);
    if (button.dataset.action === "refresh") refreshAccountStatus(account, button);
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
        void loadAnalytics();
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
      await refresh();
      void checkForUpdates(true);
    } catch (error) {
      loginError.textContent = error.message;
    } finally {
      button.disabled = false;
    }
  });

  $("#logout-button").addEventListener("click", async () => {
    try { await api("/admin/api/session", { method: "DELETE" }); } catch (_) {}
    setLogin(true, false, false);
  });
  $("#refresh-button").addEventListener("click", () => refresh(true, true));
  $("#checkin-all-button").addEventListener("click", event => manualCheckin(null, event.currentTarget));
  $("#analytics-refresh").addEventListener("click", () => loadAnalytics(true));
  $("#records-refresh").addEventListener("click", () => loadRequestRecords(true));
  document.addEventListener("visibilitychange", refreshVisibleRequestRecords);
  window.addEventListener("focus", refreshVisibleRequestRecords);
  $("#request-record-filters").addEventListener("submit", event => {
    event.preventDefault();
    state.requestRecordsPage = 1;
    state.requestRecordFilters = {
      model: $("#request-filter-model").value.trim(),
      account: $("#request-filter-account").value.trim(),
      type: $("#request-filter-type").value,
      status: $("#request-filter-status").value,
      key: $("#request-filter-key").value.trim(),
    };
    void loadRequestRecords(false);
  });
  $("#request-prev-page").addEventListener("click", () => {
    if (state.requestRecordsPage <= 1) return;
    state.requestRecordsPage -= 1;
    void loadRequestRecords(false);
  });
  $("#request-next-page").addEventListener("click", () => {
    const totalPages = state.requestRecords && hasNumber(state.requestRecords.totalPages) ? Number(state.requestRecords.totalPages) : 1;
    if (state.requestRecordsPage >= totalPages) return;
    state.requestRecordsPage += 1;
    void loadRequestRecords(false);
  });
  $("#credit-prev-page").addEventListener("click", () => {
    if (state.creditUsagePage <= 1) return;
    state.creditUsagePage -= 1;
    loadCreditUsage(false);
  });
  $("#credit-next-page").addEventListener("click", () => {
    const totalPages = state.creditUsage && hasNumber(state.creditUsage.totalPages) ? Number(state.creditUsage.totalPages) : 1;
    if (state.creditUsagePage >= totalPages) return;
    state.creditUsagePage += 1;
    loadCreditUsage(false);
  });
  $("#settings-refresh").addEventListener("click", loadSettings);
  $("#update-check").addEventListener("click", () => checkForUpdates(false));
  $("#update-install").addEventListener("click", installUpdate);
  $("#update-source-test-all").addEventListener("click", testUpdateSources);
  $("#update-custom-prefix").addEventListener("input", () => {
    const custom = state.updateSourceOptions.find(option => option.id === "custom");
    if (custom) updateUpdateSourceResult(custom, $("#update-custom-prefix").value.trim() ? null : { skipped: true });
  });
  $("#update-source-save").addEventListener("click", () => saveUpdateSource());
  document.addEventListener("pointerdown", event => {
    const disclosure = $("#update-source-disclosure");
    if (disclosure.open && !disclosure.contains(event.target)) disclosure.open = false;
  });
  document.addEventListener("keydown", event => {
    if (event.key !== "Escape") return;
    const disclosure = $("#update-source-disclosure");
    if (!disclosure.open) return;
    disclosure.open = false;
    disclosure.querySelector("summary").focus();
  });
  $("#settings-form").addEventListener("submit", saveSettings);
  $("#redemption-settings-save").addEventListener("click", saveRedemptionSettings);
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
  $("#redeem-history-refresh").addEventListener("click", loadRedemptionHistory);
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

  const modelPolicies = window.createModelPolicies({ api, element, showToast, render: renderModels });
  const admissionControl = window.createAdmission({ api, element });
  const storageManagement = window.createStorage({ api, element, formatTime });
  const jobManagement = window.createJobs({ api, element, formatTime, showToast, onComplete: () => { void refresh(false, true); void loadRedemptionHistory(); } });
  const keyManagement = window.createKeyManagement({ api, element, formatTime, formatNumber, showToast, openRecords: id => {
    state.requestRecordFilters = { model: "", account: "", type: "", status: "", key: id };
    state.requestRecordsPage = 1;
    for (const field of ["model", "account", "type", "status"]) $("#request-filter-" + field).value = "";
    $("#request-filter-key").value = id;
    selectView("records");
  } });
  boot();
})();
