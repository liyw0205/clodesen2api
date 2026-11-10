(() => {
  "use strict";
  window.createAnalyticsView = ({ $, state, api, element, modelPolicies, accountAvailable, openPlayground, formatNumber, formatTime, formatDuration, hasNumber, saveNavigation }) => {
    function formatPercent(value) {
      if (!hasNumber(value)) return "--";
      return (Number(value) * 100).toFixed(Number(value) >= 0.995 ? 1 : 1) + "%";
    }

    function modelCredits(model) {
      if (model.billingType === "token") {
        return hasNumber(model.creditsPerRequest) ? "按用量（参考 " + formatNumber(model.creditsPerRequest) + "）" : "按用量";
      }
      if (hasNumber(model.creditsPerRequest)) return formatNumber(model.creditsPerRequest) + " / 次";
      return "";
    }

    function modelRoute(id) {
      const seen = new Set();
      let enabled = true;
      while (!seen.has(id)) {
        seen.add(id);
        const rule = modelPolicies.rule(id);
        enabled = enabled && rule?.enabled !== false;
        if (!rule || rule.upstreamId === id) return { id, enabled };
        id = rule.upstreamId;
      }
      return { id, enabled: false };
    }

    function estimatedCalls(model, route) {
      if (model.billingType !== "request") return null;
      if (!route.enabled) return 0;
      const price = Number(model.creditsPerRequest);
      if (!hasNumber(model.creditsPerRequest) || price <= 0 || !state.accountsLoaded || state.modelCooldownsDegraded) return "--";
      let count = 0;
      for (const account of state.accounts) {
        if (!accountAvailable(account) || state.modelCooldowns.some(item => item.accountId === account.id && item.model.toLowerCase() === route.id.toLowerCase() && new Date(item.until).getTime() > Date.now())) continue;
        if (!hasNumber(account.balance)) return "--";
        // Account balances cannot be pooled to pay for one request.
        count += Math.floor(Math.max(0, Number(account.balance)) / price);
      }
      return formatNumber(count);
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

    function renderModels() {
      const models = state.models;
      const rows = $("#model-rows");
      const cards = $("#model-cards");
      rows.replaceChildren();
      cards.replaceChildren();
      $("#model-count").textContent = models.length ? formatNumber(models.length) + " 个模型" : "没有模型";
      $("#model-table-wrap").hidden = models.length === 0;
      $("#model-cards").hidden = models.length === 0;
      $("#model-empty").hidden = models.length > 0 || !state.modelsLoaded;
      const pricedModels = models.some(model => modelCredits(model) || modelPricingParameter(model));
      $("#model-credits-heading").hidden = !pricedModels;
      for (const model of models) {
        const id = model.id || model.name || "未命名模型";
        const route = modelRoute(id);
        const pricedModel = models.find(item => item.id === route.id) || (route.id === id ? model : {});
        const credits = modelCredits(pricedModel);
        const parameter = modelPricingParameter(pricedModel);
        const estimated = estimatedCalls(pricedModel, route);
        const row = element("tr");
        const identity = element("td", "model-id");
        const policy = modelPolicies.rule(id);
        const capability = window.modelTypes.resolve(model, models, modelPolicies.rule);
        function typeLabel() {
          const label = element("div", "model-types", capability.types.join(" / ") || (capability.known ? "无可用接口" : "类型未知"));
          label.title = capability.source + "；可在编辑策略中设置接口能力";
          return label;
        }
        identity.append(element("strong", "", policy?.publicId || id));
        identity.append(typeLabel());
        if (policy && (policy.publicId !== id || policy.upstreamId !== id)) identity.append(element("div", "field-note", id + " → " + policy.upstreamId));
        row.append(identity);
        if (pricedModels) {
          const creditCell = element("td", "model-credits");
          if (credits) creditCell.append(element("span", "model-credit-value", credits));
          if (parameter) creditCell.append(element("span", "model-credit-parameter", parameter));
          row.append(creditCell);
        }
        function controls() {
          const wrap = modelPolicies.controls(id), test = element("button", "row-action", "测试");
          test.type = "button";
          test.disabled = !route.enabled || !capability.enabled;
          test.addEventListener("click", () => openPlayground(policy?.publicId || id));
          const testAction = element("span", "model-test-action");
          testAction.append(test);
          if (estimated !== null) {
            const count = element("span", "model-call-estimate", "预计可调用 " + estimated + " 次");
            count.title = "按可用账号的余额和单次价格分别取整后汇总；余额或价格未知时暂不估算";
            testAction.append(count);
          }
          wrap.append(testAction);
          return wrap;
        }
        const actions = element("td"); actions.append(controls()); row.append(actions);
        rows.append(row);

        const card = element("article", "model-card");
        card.append(element("strong", "model-card-id", policy?.publicId || id));
        card.append(typeLabel());
        if (policy && (policy.publicId !== id || policy.upstreamId !== id)) card.append(element("div", "field-note", id + " → " + policy.upstreamId));
        const details = element("div", "model-card-details");
        if (credits) details.append(element("span", "model-credits", credits));
        if (parameter) details.append(element("span", "model-credit-parameter", parameter));
        card.append(details);
        card.append(controls());
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
      $("#credit-usage-empty").hidden = records.length > 0 || !state.creditsLoaded;
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
      $("#model-credit-stats-empty").hidden = stats.length > 0 || !state.creditsLoaded;
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
      saveNavigation(true);
      pagination.hidden = !result || totalPages <= 1;
      $("#credit-page-label").textContent = "第 " + page + " / " + totalPages + " 页";
      $("#credit-prev-page").disabled = page <= 1;
      $("#credit-next-page").disabled = page >= totalPages;
    }

    function renderUsage() {
      const usage = state.usage || {};
      $("#usage-total").textContent = formatNumber(usage.totalRequests);
      $("#usage-successes").textContent = formatNumber(usage.successes);
      $("#usage-failures").textContent = formatNumber(usage.failures);
      $("#usage-success-rate").textContent = hasNumber(usage.totalRequests) && Number(usage.totalRequests) > 0 ?
        formatPercent(Number(usage.successes || 0) / Number(usage.totalRequests)) : "--";
      $("#usage-latency").textContent = formatDuration(usage.averageLatencyMs);
      $("#usage-period").textContent = usage.startedAt ? "累计统计 · 起始于 " + formatTime(usage.startedAt) : "累计统计";
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
      const generation = state.sessionGeneration;
      $("#credit-usage-error").hidden = true;
      try {
        const result = await api(creditUsagePath(force));
        if (generation !== state.sessionGeneration) return;
        state.creditUsage = result;
        state.creditsLoaded = true;
        renderCreditModelStats();
        renderCreditUsage();
      } catch (error) {
        if (generation !== state.sessionGeneration) return;
        $("#credit-usage-error").textContent = error.message;
        $("#credit-usage-error").hidden = false;
      } finally {
        if (generation === state.sessionGeneration) state.creditUsageBusy = false;
      }
    }

    function renderCredits() {
      renderCreditModelStats();
      renderCreditUsage();
    }

    function bind() {
      $("#credit-prev-page").addEventListener("click", () => {
        if (state.creditUsagePage <= 1) return;
        state.creditUsagePage -= 1;
        saveNavigation();
        void loadCreditUsage(false);
      });
      $("#credit-next-page").addEventListener("click", () => {
        const totalPages = state.creditUsage && hasNumber(state.creditUsage.totalPages) ? Number(state.creditUsage.totalPages) : 1;
        if (state.creditUsagePage >= totalPages) return;
        state.creditUsagePage += 1;
        saveNavigation();
        void loadCreditUsage(false);
      });
    }

    return { bind, renderModels, renderUsage, renderCredits, creditUsagePath };
  };
})();
