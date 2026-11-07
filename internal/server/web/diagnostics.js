(() => {
  "use strict";
  window.createDiagnostics = ({ api, element, formatTime, formatDuration, showToast }) => {
    const $ = selector => document.querySelector(selector);
    const dialog = $("#diagnostic-dialog");
    const content = $("#diagnostic-content");
    let generation = 0, current = null;
    const errors = { upstream_429: "上游限流", insufficient_credits: "积分不足", admission_unavailable: "请求准入不可用", model_policy_denied: "模型策略拒绝", internal_panic: "服务内部异常", result_unknown: "执行结果未知" };
    async function open(record) {
      const epoch = ++generation;
      const { prompt, owner, ...safe } = record;
      current = safe;
      content.replaceChildren(); $("#diagnostic-error").textContent = "";
      const fields = element("dl", "diagnostic-fields");
      for (const [label, value] of [["请求 ID", record.requestId], ["时间", formatTime(record.createdAt)], ["阶段", ({ started: "进行中", interrupted: "已中断", finished: "已结束" })[record.stage] || (record.success ? "成功" : "失败")], ["HTTP 状态", record.status || "尚未返回"], ["端点", record.endpoint], ["公开模型", record.model], ["源模型", record.sourceModel], ["上游模型", record.upstreamModel], ["策略版本", record.policyRevision], ["账号", record.account || record.accountId], ["密钥", record.apiKeyName || record.apiKeyId], ["客户端 IP", record.clientIP], ["总耗时", formatDuration(record.durationMs)], ["首字耗时", formatDuration(record.firstTokenMs)], ["错误代码", record.authErrorCode]]) {
        fields.append(element("dt", "", label), element("dd", "", value == null || value === "" ? "--" : String(value)));
      }
      content.append(fields);
      if (record.error) content.append(element("h3", "", "错误"), element("pre", "diagnostic-error-detail", record.error));
      const attempts = element("ol", "diagnostic-attempts");
      for (const attempt of record.attempts || []) {
        const item = element("li");
        item.append(element("strong", "", attempt.account || attempt.accountId || "未选定账号"), element("p", "field-note", [attempt.model, attempt.status || "未响应", formatDuration(attempt.durationMs), errors[attempt.errorCode] || attempt.errorCode].filter(Boolean).join(" · ")), element("code", "", attempt.id));
        attempts.append(item);
      }
      content.append(element("h3", "", "上游尝试"), attempts.childElementCount ? attempts : element("p", "field-note", "没有上游尝试记录"));
      const parameters = element("details"); parameters.append(element("summary", "", "请求参数摘要"), element("pre", "diagnostic-error-detail", JSON.stringify(record.parameters || {}, null, 2))); content.append(parameters);
      const cooldowns = element("div", "diagnostic-cooldowns"); cooldowns.append(element("h3", "", "当前模型冷却"));
      const status = element("p", "field-note", "正在读取"); cooldowns.append(status); content.append(cooldowns);
      dialog.showModal();
      try {
        const result = await api("/admin/api/model-cooldowns");
        if (epoch !== generation || !dialog.open) return;
        const entries = (result.entries || []).filter(item => item.accountId === record.accountId && [record.model, record.sourceModel, record.upstreamModel].includes(item.model) && new Date(item.until).getTime() > Date.now());
        status.textContent = entries.length ? entries.map(item => item.model + " · " + (errors[item.reason] || item.reason) + " · 恢复 " + formatTime(item.until)).join("\n") : "当前没有匹配的模型冷却";
        if (result.degraded) status.textContent += " · 冷却存储状态异常";
      } catch (error) { if (epoch === generation && dialog.open) status.textContent = "读取失败：" + error.message; }
    }
    function control(record) {
      const button = element("button", "text-button", "诊断"); button.type = "button"; button.setAttribute("aria-label", "诊断请求 " + (record.requestId || record.id));
      button.addEventListener("click", () => { void open(record); }); return button;
    }
    function clear() { generation++; current = null; content.replaceChildren(); $("#diagnostic-error").textContent = ""; dialog.close(); }
    dialog.addEventListener("close", () => { if (!dialog.open) clear(); });
    dialog.addEventListener("cancel", event => { event.preventDefault(); clear(); });
    dialog.querySelectorAll("[data-close]").forEach(button => button.addEventListener("click", clear));
    $("#diagnostic-copy").addEventListener("click", async () => {
      if (!current) return;
      const epoch = generation;
      try { await navigator.clipboard.writeText(JSON.stringify(current, null, 2)); if (epoch === generation) showToast("诊断信息已复制"); }
      catch (_) { if (epoch === generation) $("#diagnostic-error").textContent = "复制失败，请检查剪贴板权限。"; }
    });
    return { control, clear };
  };
})();
