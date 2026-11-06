(() => {
  "use strict";
  window.createKeyManagement = ({ api, element, formatTime, formatNumber, showToast, openRecords }) => {
    const $ = selector => document.querySelector(selector);
    let keys = [];
    let editing = null;
    let pending = null;
    let loading = null;
    let generation = 0;
    let busy = false;
    const editor = $("#key-editor");
    const secretDialog = $("#key-secret-dialog");
    const confirmDialog = $("#key-confirm-dialog");
    const errorNotice = (selector, message) => {
      $(selector).textContent = message || "";
      $(selector).hidden = !message;
    };
    function status(item) {
      if (!item.enabled) return ["已停用", "status-paused"];
      if (item.expiresAt && new Date(item.expiresAt).getTime() <= Date.now()) return ["已过期", "status-cooldown"];
      return ["启用", "status-ready"];
    }
    function actions(item) {
      const wrap = element("div", "key-actions");
      for (const [label, action, danger] of [["编辑", "edit"], [item.enabled ? "停用" : "启用", "toggle"], ["记录", "records"], ["轮换", "rotate", true], ["删除", "delete", true]]) {
        const button = element("button", "row-action" + (danger ? " row-action-danger" : ""), label);
        button.type = "button";
        button.disabled = busy;
        button.addEventListener("click", () => {
          if (action === "edit") void openEditor(item);
          else if (action === "records") openRecords(item.id);
          else if (action === "toggle") void toggle(item);
          else confirm(item, action);
        });
        wrap.append(button);
      }
      return wrap;
    }
    function details(item) {
      const values = ["RPM " + (item.rpm ?? "不限"), "并发 " + (item.limits?.maxConcurrent || "不限"), "日请求 " + (item.quota?.dailyUsed || 0) + " / " + (item.limits?.dailyRequests || "不限"), "月请求 " + (item.quota?.monthlyUsed || 0) + " / " + (item.limits?.monthlyRequests || "不限"), "到期 " + (item.expiresAt ? formatTime(item.expiresAt) : "永久"), "IP " + (item.ipAllow?.join(", ") || "不限"), "模型 " + (item.models?.join(", ") || "全部")];
      if (item.quota?.dailyResetAt) values.push("日重置 " + formatTime(item.quota.dailyResetAt));
      for (const [period, label] of [["daily", "日"], ["monthly", "月"]]) {
        const balance = item.quota?.[period + "Metering"] || {};
        for (const [unit, text] of [["Tokens", "Token"], ["Credits", "积分"]]) {
          if (item.limits?.[period + unit]) values.push(label + text + " " + (balance["used" + unit] || 0) + " + 预留 " + (balance["reserved" + unit] || 0) + " / " + item.limits[period + unit] + " · 剩余 " + Math.max(0,item.limits[period+unit]-(balance["used"+unit]||0)-(balance["reserved"+unit]||0)));
        }
      }
      return values;
    }
    function render() {
      const rows = $("#keys-rows");
      const cards = $("#keys-cards");
      rows.replaceChildren(); cards.replaceChildren();
      $("#keys-count").textContent = keys.length + " 个密钥";
      $("#keys-empty").hidden = keys.length > 0;
      $("#keys-table-wrap").hidden = keys.length === 0;
      cards.hidden = keys.length === 0;
      for (const item of keys) {
        const [label, className] = status(item);
        const identity = element("td", "key-identity");
        identity.append(element("strong", "", item.name), element("code", "", item.prefix + "…"));
        const state = element("td");
        state.append(element("span", "status-badge " + className, label));
        const limits = element("td", "key-limits");
        for (const value of details(item)) limits.append(element("span", "", value));
        const usage = item.usage || {};
        const counts = formatNumber(usage.requests || 0) + " 次 · 成功 " + formatNumber(usage.successes || 0) + " · 失败 " + formatNumber(usage.failures || 0);
        const times = element("td", "key-times");
        times.append(element("span", "", formatTime(item.lastUsedAt)), element("small", "muted", formatTime(item.createdAt)));
        const commands = element("td"); commands.append(actions(item));
        const row = element("tr"); row.append(identity, state, limits, element("td", "", counts), times, commands); rows.append(row);
        const card = element("article", "key-card");
        const top = element("div", "key-card-top"); top.append(element("strong", "", item.name), element("span", "status-badge " + className, label));
        const meta = element("div", "key-limits");
        meta.append(element("code", "", item.prefix + "…"));
        for (const value of details(item)) meta.append(element("span", "", value));
        card.append(top, meta, element("p", "field-note", counts), element("p", "field-note", "最近 " + formatTime(item.lastUsedAt) + " · 创建 " + formatTime(item.createdAt)), actions(item));
        cards.append(card);
      }
    }
    function load() {
      if (loading) return loading;
      const current = generation;
      $("#keys-refresh").disabled = true;
      errorNotice("#keys-error", "");
      loading = api("/admin/api/keys").then(result => {
        if (current !== generation) return;
        if (!result || !Array.isArray(result.keys) || result.keys.some(item => typeof item.id !== "string" || typeof item.version !== "number")) throw new Error("密钥列表格式无效");
        keys = result.keys;
        const notes = [];
        if (result.externalConfigured) notes.push("外部配置 key 已启用；该 key 不受单个密钥限制。");
        if (result.allowNoAuth) notes.push("未配置任何 key 时允许匿名访问。");
        errorNotice("#keys-deployment", notes.join(" "));
        render();
      }).catch(error => {
        if (current === generation) errorNotice("#keys-error", error.message);
      }).finally(() => {
        if (current === generation) { loading = null; $("#keys-refresh").disabled = false; }
      });
      return loading;
    }
    async function openEditor(item = null) {
      if (busy) return;
      editing = item;
      $("#key-form").reset();
      $("#key-name").value = item?.name || "";
      $("#key-enabled").checked = item?.enabled !== false;
      $("#key-enabled").disabled = !item;
      $("#key-rpm").value = item?.rpm ?? "";
      $("#key-concurrent").value = item?.limits?.maxConcurrent || "";
      $("#key-daily").value = item?.limits?.dailyRequests || "";
      $("#key-monthly").value = item?.limits?.monthlyRequests || "";
      for (const field of ["dailyTokens", "monthlyTokens", "dailyCredits", "monthlyCredits"]) $("#key-" + field).value = item?.limits?.[field] || "";
      $("#key-ip").value = item?.ipAllow?.join("\n") || "";
      if (item?.expiresAt) {
        const date = new Date(item.expiresAt);
        $("#key-expiry").value = new Date(date.getTime() - date.getTimezoneOffset() * 60000).toISOString().slice(0, 16);
      }
      errorNotice("#key-form-error", "");
      $("#key-editor-title").textContent = item ? "编辑密钥" : "创建密钥";
      $("#key-save").textContent = item ? "保存" : "创建密钥";
      const options = new Set(item?.models || []);
      const labels = new Map();
      const renderOptions = () => {
        $("#key-models").replaceChildren(...Array.from(options).sort().map(id => {
          const option = element("option", "", labels.get(id) || id); option.value = id; option.selected = Boolean(item?.models?.includes(id)); return option;
        }));
      };
      renderOptions();
      editor.showModal();
      const current = generation;
      try {
        const result = await api("/admin/api/models");
        if (current !== generation || !editor.open || editing !== item) return;
        const selected = new Set(Array.from($("#key-models").selectedOptions, option => option.value));
        for (const model of result.data || result.models || []) if (typeof model.id === "string") options.add(model.id);
        const policies = await api("/admin/api/model-policies");
        if (current !== generation || !editor.open || editing !== item) return;
        for (const model of policies.models || []) {
          options.add(model.sourceId);
          labels.set(model.sourceId, model.publicId + (model.enabled ? "" : "（全局停用）"));
        }
        renderOptions();
        for (const option of $("#key-models").options) option.selected = selected.has(option.value);
      } catch (_) { /* Manually entered IDs and existing selections remain editable. */ }
    }
    function showSecret(secret) {
      $("#key-secret").value = secret;
      errorNotice("#key-copy-error", "");
      secretDialog.showModal();
    }
    async function conflict(error) {
      if (error.status === 409) {
        editor.close(); confirmDialog.close();
        await load();
        errorNotice("#keys-error", "密钥已被其他操作更新，请重新编辑。");
      }
    }
    async function save(event) {
      event.preventDefault();
      if (busy) return;
      const current = generation;
      const item = editing;
      const rpm = $("#key-rpm").value.trim();
      const expiry = $("#key-expiry").value;
      const models = [...Array.from($("#key-models").selectedOptions, option => option.value), ...$("#key-custom-models").value.split(/[,\n]/).map(value => value.trim()).filter(Boolean)];
      const payload = { name: $("#key-name").value.trim(), rpm: rpm ? Number(rpm) : null, expiresAt: expiry ? new Date(expiry).toISOString() : null, ipAllow: $("#key-ip").value.split(/[,\n]/).map(value => value.trim()).filter(Boolean), models: Array.from(new Set(models)) };
      payload.limits = { ...(item?.limits || {}), maxConcurrent: Number($("#key-concurrent").value || 0), dailyRequests: Number($("#key-daily").value || 0), monthlyRequests: Number($("#key-monthly").value || 0) };
      for (const field of ["dailyTokens", "monthlyTokens", "dailyCredits", "monthlyCredits"]) payload.limits[field] = Number($("#key-" + field).value || 0);
      if (item) { payload.version = item.version; payload.enabled = $("#key-enabled").checked; }
      busy = true; $("#key-save").disabled = true; errorNotice("#key-form-error", "");
      try {
        const result = await api("/admin/api/keys" + (item ? "/" + encodeURIComponent(item.id) : ""), { method: item ? "PUT" : "POST", body: JSON.stringify(payload) });
        if (current !== generation) return;
        editor.close();
        if (result.secret) showSecret(result.secret);
        showToast(item ? "密钥已更新" : "密钥已创建");
        await load();
      } catch (error) {
        if (current === generation) { errorNotice("#key-form-error", error.message); await conflict(error); }
      } finally { if (current === generation) { busy = false; $("#key-save").disabled = false; render(); } }
    }
    async function toggle(item) {
      if (busy) return;
      const current = generation;
      busy = true; render();
      try {
        await api("/admin/api/keys/" + encodeURIComponent(item.id), { method: "PUT", body: JSON.stringify({ version: item.version, enabled: !item.enabled }) });
        if (current !== generation) return;
        await load();
      } catch (error) { if (current === generation) { errorNotice("#keys-error", error.message); await conflict(error); } }
      finally { if (current === generation) { busy = false; render(); } }
    }
    function confirm(item, action) {
      pending = { item, action };
      $("#key-confirm-title").textContent = action === "rotate" ? "轮换密钥" : "删除密钥";
      $("#key-confirm-copy").textContent = action === "rotate" ? "轮换「" + item.name + "」后，旧密钥立即失效。" : "删除「" + item.name + "」后，客户端将无法继续使用该密钥。";
      errorNotice("#key-confirm-error", ""); confirmDialog.showModal();
    }
    async function execute() {
      if (!pending || busy) return;
      const { item, action } = pending;
      const current = generation;
      busy = true; $("#key-confirm-submit").disabled = true;
      try {
        const result = await api("/admin/api/keys/" + encodeURIComponent(item.id) + (action === "rotate" ? "/rotate" : ""), { method: action === "rotate" ? "POST" : "DELETE", body: JSON.stringify({ version: item.version }) });
        if (current !== generation) return;
        confirmDialog.close();
        if (result?.secret) showSecret(result.secret);
        showToast(action === "rotate" ? "密钥已轮换" : "密钥已删除"); await load();
      } catch (error) { if (current === generation) { errorNotice("#key-confirm-error", error.message); await conflict(error); } }
      finally { if (current === generation) { busy = false; $("#key-confirm-submit").disabled = false; render(); } }
    }
    function clear() {
      generation++; keys = []; loading = null; editing = null; pending = null; busy = false;
      editor.close(); secretDialog.close(); confirmDialog.close();
      $("#key-secret").value = ""; $("#key-form").reset();
      $("#key-save").disabled = false; $("#key-confirm-submit").disabled = false;
      $("#keys-refresh").disabled = false; render();
    }
    $("#keys-refresh").addEventListener("click", load);
    $("#keys-create").addEventListener("click", () => openEditor());
    $("#key-form").addEventListener("submit", save);
    $("#key-confirm-submit").addEventListener("click", execute);
    secretDialog.addEventListener("close", () => { $("#key-secret").value = ""; });
    secretDialog.addEventListener("cancel", () => { $("#key-secret").value = ""; });
    secretDialog.querySelectorAll("[data-close]").forEach(button => button.addEventListener("click", () => { $("#key-secret").value = ""; }));
    $("#key-copy").addEventListener("click", async () => {
      const current = generation;
      try { await navigator.clipboard.writeText($("#key-secret").value); if (current === generation && secretDialog.open) showToast("密钥已复制"); }
      catch (_) { if (current === generation && secretDialog.open) { $("#key-secret").select(); errorNotice("#key-copy-error", "自动复制失败，请复制已选中的密钥。"); } }
    });
    return { load, clear };
  };
})();
