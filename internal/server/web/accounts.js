(() => {
  "use strict";
  window.createAccounts = ({ $, api, state, element, formatNumber, formatTime, formatChinaTime, hasNumber, showToast, routingTools, visibleAccounts, updateTools, submitJob }) => {
    const accountForm = $("#account-form");
    const accountDialog = $("#account-dialog");
    const confirmDialog = $("#confirm-dialog");

    function accountState(account) {
      const cooldownUntil = account.cooldownUntil ? new Date(account.cooldownUntil) : null;
      if (!account.enabled) return { text: "已停用", className: "status-paused" };
      if (cooldownUntil && cooldownUntil > new Date()) return { text: cooldownLabel(account) + "，冷却至 " + formatTime(account.cooldownUntil), className: "status-cooldown" };
      if (creditRecheckPending(account)) return { text: "等待积分复查", className: "status-cooldown" };
      if (account.error) return { text: "状态异常", className: "status-error" };
      return { text: "可用", className: "status-ready" };
    }

    function render() {
      const enabled = state.accounts.filter(account => account.enabled);
      const cooling = enabled.filter(account => (account.cooldownUntil && new Date(account.cooldownUntil) > new Date()) || creditRecheckPending(account));
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
      routingTools.set(state.strategy);
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

    function creditRecheckPending(account) {
      return Boolean(account.cooldownUntil && account.cooldownReason !== "temporary_ban" && new Date(account.cooldownUntil) <= new Date());
    }

    function accountDetail(account) {
      const cooldownUntil = account.cooldownUntil ? new Date(account.cooldownUntil) : null;
      if (cooldownUntil && cooldownUntil > new Date()) return cooldownLabel(account) + "，冷却至 " + formatTime(account.cooldownUntil);
      if (creditRecheckPending(account)) return "等待积分复查";
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
      checkbox.dataset.selectAccount = account.id;
      checkbox.setAttribute("aria-label", "选择 " + account.identifier);
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
      for (const account of visibleAccounts()) {
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
      for (const account of visibleAccounts()) {
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
      const visible = visibleAccounts();
      const count = visible.filter(account => state.selected.has(account.id)).length;
      control.checked = visible.length > 0 && count === visible.length;
      control.indeterminate = count > 0 && count < visible.length;
      control.disabled = visible.length === 0;
      updateTools();
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
          const knownIDs = new Set(state.accounts.map(account => account.id));
          for (const id of Array.from(state.selected)) if (!knownIDs.has(id)) state.selected.delete(id);
          render();
        } catch (error) {
          if (generation !== state.sessionGeneration) return;
          if (!silent && error.message !== "登录状态已过期，请重新登录") {
            $("#error-state").textContent = error.message;
            $("#error-state").hidden = false;
          }
        } finally {
          if (!silent && generation === state.sessionGeneration) {
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

    async function saveAccount(event) {
      event.preventDefault();
      const epoch = state.sessionGeneration;
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
        if (epoch !== state.sessionGeneration) return;
        accountDialog.close();
        showToast(id ? "账号已更新" : "账号已添加");
        await refresh(false, true);
      } catch (error) {
        if (epoch === state.sessionGeneration) $("#account-form-error").textContent = error.message;
      } finally {
        if (epoch === state.sessionGeneration) button.disabled = false;
      }
    }

    async function changeEnabled(account, enabled) {
      const epoch = state.sessionGeneration;
      try {
        await api("/admin/api/accounts/" + encodeURIComponent(account.id), {
          method: "PATCH",
          body: JSON.stringify({ enabled }),
        });
        if (epoch !== state.sessionGeneration) return;
        showToast(enabled ? "账号已启用" : "账号已停用");
        await refresh(false, true);
      } catch (error) {
        if (epoch === state.sessionGeneration) showToast(error.message);
      }
    }

    async function removeAccount() {
      const epoch = state.sessionGeneration;
      const id = state.deletingID;
      if (!id) return;
      const button = $("#confirm-delete");
      button.disabled = true;
      try {
        await api("/admin/api/accounts/" + encodeURIComponent(id), { method: "DELETE" });
        if (epoch !== state.sessionGeneration) return;
        confirmDialog.close();
        state.selected.delete(id);
        showToast("账号已移除");
        await refresh(false, true);
      } catch (error) {
        if (epoch === state.sessionGeneration) $("#confirm-copy").textContent = error.message;
      } finally {
        if (epoch === state.sessionGeneration) button.disabled = false;
      }
    }

    async function manualCheckin(account = null, button = null) {
      const epoch = state.sessionGeneration;
      if (button) button.disabled = true;
      if (!account) $("#checkin-all-button").disabled = true;
      try {
        await submitJob("checkin", account ? [account.id] : []);
      } catch (error) {
        if (epoch === state.sessionGeneration) showToast(error.message);
      } finally {
        if (button) button.disabled = false;
        if (!account) $("#checkin-all-button").disabled = false;
      }
    }

    async function refreshAccountStatus(account, button = null) {
      const epoch = state.sessionGeneration;
      if (button) button.disabled = true;
      try {
        await submitJob("refresh", [account.id]);
      } catch (error) {
        if (epoch === state.sessionGeneration) showToast(error.message);
      } finally {
        if (button) button.disabled = false;
      }
    }

    async function updateStrategy(strategy) {
      if (strategy === state.strategy) return;
      const epoch = state.sessionGeneration;
      routingTools.busy(true);
      try {
        const result = await api("/admin/api/settings", { method: "PUT", body: JSON.stringify({ strategy }) });
        if (epoch !== state.sessionGeneration) return;
        state.strategy = result.strategy;
        renderStrategies();
        showToast(({ round_robin: "已切换为轮询选择", random: "已切换为随机选择", fixed: "已切换为固定选择" })[strategy] || "账号策略已更新");
      } catch (error) {
        if (epoch === state.sessionGeneration) { routingTools.set(state.strategy); showToast(error.message); }
      } finally {
        if (epoch === state.sessionGeneration) routingTools.busy(false);
      }
    }

    function openDeleteDialog(account) {
      state.deletingID = account.id;
      $("#confirm-copy").textContent = "确定移除 " + account.identifier + "？该账号将立即从账号列表中删除。";
      confirmDialog.showModal();
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

    function bind() {
      $("#refresh-button").addEventListener("click", () => refresh(true, true));
      $("#checkin-all-button").addEventListener("click", event => manualCheckin(null, event.currentTarget));
      $("#account-open").addEventListener("click", () => openAccountDialog());
      $("#empty-add").addEventListener("click", () => openAccountDialog());
      accountForm.addEventListener("submit", saveAccount);
      $("#confirm-delete").addEventListener("click", removeAccount);
      $("#account-rows").addEventListener("click", handleAccountAction);
      $("#account-cards").addEventListener("click", handleAccountAction);
      document.addEventListener("change", event => {
        const checkbox = event.target;
        if (checkbox.matches("[data-select-account]")) {
          if (checkbox.checked) state.selected.add(checkbox.dataset.selectAccount);
          else state.selected.delete(checkbox.dataset.selectAccount);
          for (const input of document.querySelectorAll("[data-select-account]")) input.checked = state.selected.has(input.dataset.selectAccount);
          updateSelectAll();
          return;
        }
        if (checkbox.id === "select-all") {
          visibleAccounts().forEach(account => {
            if (checkbox.checked) state.selected.add(account.id);
            else state.selected.delete(account.id);
          });
          renderList();
        }
      });
    }

    function renderList() {
      renderTable();
      renderCards();
      updateSelectAll();
    }

    return { bind, accountState, render, renderList, refresh, updateStrategy };
  };
})();
