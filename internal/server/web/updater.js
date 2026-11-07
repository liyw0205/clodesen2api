(() => {
  "use strict";
  window.createUpdater = ({ $, state, element, api, loadSettings, showToast }) => {
    let restartProbe = null;
    const sourcePanel = window.createFloatingPanel({ disclosure: $("#update-source-disclosure"), panel: $("#update-source-panel") });
    function revealCustomSource() {
      const body = $(".update-source-body");
      body.scrollTop = body.scrollHeight;
      sourcePanel.position();
    }
    $("#update-source-disclosure").addEventListener("toggle", event => {
      if (event.currentTarget.open && state.updateSourceID === "custom") revealCustomSource();
    });
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
          revealCustomSource();
          $("#update-custom-prefix").focus({ preventScroll: true });
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
    const epoch = state.sessionGeneration;
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
      if (epoch !== state.sessionGeneration) return;
      completed += 1;
      if (result.ok) available += 1;
      updateUpdateSourceResult(option, result);
      $("#update-source-result").textContent = "正在测速 " + completed + "/" + options.length;
    }));
    if (epoch !== state.sessionGeneration) return;
    state.updateSourceTesting = false;
    button.disabled = false;
    $("#update-source-save").disabled = false;
    $("#update-source-result").textContent = "测速完成：" + available + "/" + options.length + " 个源可用";
  }

  async function checkForUpdates(silent = false) {
    const epoch = state.sessionGeneration;
    const button = $("#update-check");
    button.disabled = true;
    if (!silent) $("#update-status").textContent = "正在检查版本";
    try {
      const status = await api("/admin/api/update");
      if (epoch === state.sessionGeneration) renderUpdateStatus(status);
    } catch (error) {
      if (epoch !== state.sessionGeneration) return;
      if (error.message === "登录状态已过期，请重新登录") throw error;
      $("#update-status").textContent = "检查更新失败，请稍后重试";
    } finally {
      if (epoch === state.sessionGeneration) button.disabled = false;
    }
  }

  async function installUpdate() {
    const epoch = state.sessionGeneration;
    const button = $("#update-install");
    button.disabled = true;
    $("#update-status").textContent = "正在下载并校验更新包，耗时可能受网络影响";
    try {
      const result = await api("/admin/api/update", { method: "POST", body: "{}", timeout: 180000 });
      if (epoch !== state.sessionGeneration) return;
      $("#update-status").textContent = result.message || "更新已安装，服务即将重启";
      showToast($("#update-status").textContent);
      const restarted = await waitForServiceRestart();
      if (epoch !== state.sessionGeneration) return;
      if (restarted) {
        $("#update-status").textContent = "服务已恢复，正在刷新页面";
        showToast($("#update-status").textContent);
        window.location.reload();
      } else {
        $("#update-status").textContent = "服务重启检测超时，请手动刷新页面";
        showToast($("#update-status").textContent);
        button.disabled = false;
      }
    } catch (error) {
      if (epoch !== state.sessionGeneration) return;
      $("#update-status").textContent = error.message;
      button.disabled = false;
    }
  }

  async function waitForServiceRestart() {
    const epoch = state.sessionGeneration;
    const startedAt = Date.now();
    const timeout = 15000;
    const interval = 500;
    let unavailable = false;
    while (Date.now() - startedAt < timeout && epoch === state.sessionGeneration) {
      $("#update-status").textContent = unavailable ? "正在等待服务恢复" : "正在等待服务重启";
      const remaining = timeout - (Date.now() - startedAt);
      if (remaining <= 0) break;
      const controller = new AbortController();
      restartProbe = controller;
      const abortTimer = window.setTimeout(() => controller.abort(), Math.min(interval, remaining));
      try {
        const response = await window.fetch("/healthz", {
          credentials: "same-origin",
          cache: "no-store",
          signal: controller.signal,
        });
        if (response.ok) {
          if (unavailable) return true;
        } else {
          unavailable = true;
        }
      } catch {
        unavailable = true;
      } finally {
        window.clearTimeout(abortTimer);
        if (restartProbe === controller) restartProbe = null;
      }
      const delay = timeout - (Date.now() - startedAt);
      if (delay <= 0) break;
      await new Promise(resolve => window.setTimeout(resolve, Math.min(interval, delay)));
    }
    return false;
  }

    return { renderUpdateSourceOptions, updateUpdateSourceResult, checkForUpdates, installUpdate, testUpdateSources, saveUpdateSource, clear() {
      sourcePanel.close();
      restartProbe?.abort(); restartProbe = null;
      state.updateSourceTesting = false; state.updateSourceResults = {}; state.updateSourceOptions = []; state.updateSourceID = "direct";
      for (const id of ["update-status", "update-source-result", "update-source-options", "update-notes"]) $("#" + id).replaceChildren();
    } };
  };
})();
