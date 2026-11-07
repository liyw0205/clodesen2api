(() => {
  "use strict";
  window.createAnalytics = ({ api, element, state, modelPolicies, renderModels, renderUsage, renderCredits, creditUsagePath, formatTime }) => {
    const $ = selector => document.querySelector(selector);
    let pending = null;
    const revisions = { models: 0, usage: 0, credits: 0 };
    const resources = {
      models: { error: "#model-error", target: "#model-table-wrap", load: force => api("/admin/api/models" + (force ? "?refresh=1" : "")), accept(value) { state.models = Array.isArray(value.data) ? value.data : []; state.modelsLoaded = true; renderModels(); } },
      usage: { error: "#usage-error", target: ".analytics-summary", load: () => api("/admin/api/usage"), accept(value) { state.usage = value; renderUsage(); } },
      credits: { error: "#credit-usage-error", target: "#credit-usage-table-wrap", load: force => api(creditUsagePath(force)), accept(value) { state.creditUsage = value; state.creditsLoaded = true; renderCredits(); } },
    };
    const notices = new Map();
    function notice(name) {
      if (!notices.has(name)) {
        const node = element("p", "field-note resource-status"); node.setAttribute("role", "status");
        $(resources[name].error).after(node); notices.set(name, node);
      }
      return notices.get(name);
    }
    async function loadResource(name, force = false) {
      const resource = resources[name], epoch = state.sessionGeneration, revision = ++revisions[name];
      const error = $(resource.error), status = notice(name), target = $(resource.target);
      const previous = status.dataset.updated;
      error.hidden = true;
      status.textContent = "正在加载" + (previous ? " · 上次成功 " + formatTime(previous) : "");
      target.setAttribute("aria-busy", "true");
      try {
        const results = await Promise.allSettled([resource.load(force), ...(name === "models" ? [modelPolicies.load()] : [])]);
        if (epoch !== state.sessionGeneration || revision !== revisions[name]) return;
        if (results[0].status === "rejected") throw results[0].reason;
        resource.accept(results[0].value);
        state.analyticsLoaded = true;
        status.dataset.updated = new Date().toISOString();
        status.textContent = "最近更新 " + formatTime(status.dataset.updated);
        if (results[1]?.status === "rejected") throw new Error("模型策略加载失败：" + results[1].reason.message);
      } catch (failure) {
        if (epoch !== state.sessionGeneration || revision !== revisions[name]) return;
        error.replaceChildren(element("span", "", failure.message));
        const retry = element("button", "text-button", "重试"); retry.type = "button";
        retry.addEventListener("click", () => { void loadResource(name, true); });
        error.append(retry); error.hidden = false;
        const updated = status.dataset.updated;
        status.textContent = updated ? "显示上次成功数据 · " + formatTime(updated) : "尚未加载成功";
      } finally {
        if (epoch === state.sessionGeneration && revision === revisions[name]) target.setAttribute("aria-busy", "false");
      }
    }
    function load(force = false) {
      if (pending) return pending;
      const epoch = state.sessionGeneration;
      $("#analytics-refresh").disabled = true;
      const task = Promise.all(Object.keys(resources).map(name => loadResource(name, force))).finally(() => {
        if (pending === task) pending = null;
        if (epoch === state.sessionGeneration) $("#analytics-refresh").disabled = false;
      });
      pending = task;
      return task;
    }
    return { load, clear() { pending = null; for (const name of Object.keys(resources)) { revisions[name]++; $(resources[name].target).setAttribute("aria-busy", "false"); } for (const node of notices.values()) { node.textContent = ""; delete node.dataset.updated; } $("#analytics-refresh").disabled = false; } };
  };
})();
