(() => {
  "use strict";
  window.createRequestRecords = ({ api, $, element, state, dashboard, diagnostics, formatNumber, formatTime, formatDuration, hasNumber, saveNavigation }) => {
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
          $("#request-error-dialog").showModal();
        });
        return button;
      }
      return element("span", "record-status-" + category, label);
    }

    function promptAction(record) {
      const button = element("button", "prompt-button", record.prompt ? "查看提示词" : "无提示词");
      button.type = "button";
      button.disabled = !record.prompt;
      if (record.prompt) button.addEventListener("click", () => {
        $("#request-prompt-text").textContent = record.prompt;
        $("#prompt-dialog").showModal();
      });
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

    function renderPagination(result) {
      const pagination = $("#request-record-pagination");
      const totalPages = result && hasNumber(result.totalPages) ? Number(result.totalPages) : 1;
      const page = result && hasNumber(result.page) ? Number(result.page) : state.requestRecordsPage;
      state.requestRecordsPage = page;
      saveNavigation(true);
      pagination.hidden = !result || totalPages <= 1;
      $("#request-page-label").textContent = "第 " + page + " / " + totalPages + " 页";
      $("#request-prev-page").disabled = page <= 1;
      $("#request-next-page").disabled = page >= totalPages;
    }

    function render() {
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
      if (selectedAccount && !accountOptions.some(option => option.value === selectedAccount)) accountFilter.append(new Option(selectedAccount, selectedAccount));
      accountFilter.value = selectedAccount;
      rows.replaceChildren();
      cards.replaceChildren();
      $("#request-record-empty").hidden = records.length > 0 || !result;
      $("#request-record-table-wrap").hidden = records.length === 0;
      cards.hidden = records.length === 0;
      renderPagination(result);

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
        client.append(diagnostics.control(record));
        const attempts = attemptDetails(record);
        if (attempts) client.append(attempts);
        row.append(element("td", "request-record-time", formatTime(record.createdAt)),
          element("td", "model-id request-record-model", record.model || "未指定"),
          element("td", "request-record-type " + (record.type || ""), requestTypeLabel(record.type)),
          element("td", "request-record-endpoint", record.endpoint || "--"),
          element("td", "request-record-account", record.account || record.accountId || "--"),
          element("td", "request-record-duration-cell", formatDuration(record.durationMs)),
          firstCell, thinkingCell, statusCell,
          element("td", "request-record-parameters", params), client);
        rows.append(row);

        const card = element("article", "request-record-card");
        const top = element("div", "request-record-card-top");
        top.append(element("strong", "model-id", record.model || "未指定"), requestStatusNode(record), diagnostics.control(record));
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

    function path() {
      const params = new URLSearchParams({ page: String(state.requestRecordsPage), pageSize: String(state.requestRecordsPageSize) });
      for (const [key, value] of Object.entries(state.requestRecordFilters)) if (value) params.set(key, value);
      return "/admin/api/request-records?" + params.toString();
    }

    async function load(force = false) {
      const generation = state.sessionGeneration;
      if (state.requestRecordsController) state.requestRecordsController.abort();
      const controller = new AbortController();
      const requestID = ++state.requestRecordsRequestID;
      state.requestRecordsController = controller;
      const button = $("#records-refresh");
      const oldLabel = button.textContent;
      button.disabled = true;
      $("#request-record-error").hidden = true;
      try {
        const result = await api(path() + (force ? "&refresh=1" : ""), { signal: controller.signal });
        if (requestID !== state.requestRecordsRequestID || generation !== state.sessionGeneration) return;
        state.requestRecords = result;
        render();
      } catch (error) {
        if (error.name === "AbortError" || requestID !== state.requestRecordsRequestID) return;
        if (generation !== state.sessionGeneration) return;
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

    function refreshVisible() {
      if (dashboard.hidden || document.hidden || $("#records-view").hidden) return;
      const now = Date.now();
      if (now - state.requestRecordsLastRefreshAt < 1500) return;
      state.requestRecordsLastRefreshAt = now;
      void load(true);
    }

    function bind() {
      for (const [dialogID, textID] of [["prompt-dialog", "request-prompt-text"], ["request-error-dialog", "request-error-text"]]) {
        const dialog = $("#" + dialogID);
        const clear = () => { $("#" + textID).textContent = ""; };
        dialog.addEventListener("close", () => { if (!dialog.open) clear(); });
        dialog.addEventListener("cancel", clear);
        for (const button of dialog.querySelectorAll("[data-close]")) button.addEventListener("click", clear);
      }
      $("#records-refresh").addEventListener("click", () => load(true));
      document.addEventListener("visibilitychange", refreshVisible);
      window.addEventListener("focus", refreshVisible);
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
        saveNavigation();
        void load(false);
      });
      $("#request-prev-page").addEventListener("click", () => {
        if (state.requestRecordsPage <= 1) return;
        state.requestRecordsPage -= 1;
        saveNavigation();
        void load(false);
      });
      $("#request-next-page").addEventListener("click", () => {
        const totalPages = state.requestRecords && hasNumber(state.requestRecords.totalPages) ? Number(state.requestRecords.totalPages) : 1;
        if (state.requestRecordsPage >= totalPages) return;
        state.requestRecordsPage += 1;
        saveNavigation();
        void load(false);
      });
    }

    function clear() {
      state.requestRecordsRequestID++;
      state.requestRecordsController?.abort();
      state.requestRecordsController = null;
      state.requestRecords = null;
      state.requestRecordsLastRefreshAt = 0;
      $("#records-refresh").disabled = false;
      $("#request-record-rows").replaceChildren();
      $("#request-record-cards").replaceChildren();
      for (const id of ["request-prompt-text", "request-error-text", "request-record-error", "audit-storage-health"]) {
        const node = $("#" + id);
        if (node) node.textContent = "";
      }
      for (const id of ["request-record-total", "request-record-successes", "request-record-failures", "request-record-duration", "request-record-first-token"]) $("#" + id).textContent = "--";
      $("#request-record-updated").textContent = "最近更新 --";
    }

    return { bind, load, render, clear };
  };
})();
