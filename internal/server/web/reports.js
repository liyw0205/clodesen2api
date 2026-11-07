(() => {
  "use strict";
  window.createReports = ({ api, element, formatTime }) => {
    const root = element("section", "analytics-section"); root.id = "reports-panel";
    root.append(element("h2", "", "时间范围报表"));
    const controls = element("div", "settings-grid"), range = element("select"), dimension = element("select");
    range.id = "report-range"; dimension.id = "report-dimension";
    for (const [value, label] of [["today", "今天"], ["7d", "近 7 天"], ["30d", "近 30 天"]]) range.append(new Option(label, value));
    for (const [value, label] of [["day", "日期"], ["model", "模型"], ["account", "账号 ID"], ["key", "密钥 ID"]]) dimension.append(new Option(label, value));
    const field = (label, input) => { const node = element("label", "", label); node.append(input); return node; };
    controls.append(field("时间范围", range), field("分组", dimension));
    const refresh = element("button", "button button-secondary", "刷新报表"), download = element("button", "button button-secondary", "导出 CSV");
    refresh.type = download.type = "button"; refresh.id = "report-refresh"; download.id = "report-export";
    const actions = element("div", "heading-actions"); actions.append(refresh, download);
    const note = element("p", "field-note"), error = element("p", "form-error"), wrap = element("div", "table-wrap"), table = element("table", "account-table"), head = element("thead"), header = element("tr"), body = element("tbody");
    note.id = "report-coverage"; note.setAttribute("role", "status"); error.id = "report-error"; error.setAttribute("role", "alert"); body.id = "report-rows";
    for (const label of ["分组", "请求", "成功率", "平均耗时", "P95", "积分估算"]) header.append(element("th", "", label));
    head.append(header); table.append(head, body); wrap.append(table); root.append(controls, actions, note, error, wrap);
    document.querySelector("#usage-error").after(root);
    let generation = 0, controller = null;
    const path = () => "/admin/api/reports?" + new URLSearchParams({ range: range.value, dimension: dimension.value });
    async function load() {
      const epoch = ++generation; controller?.abort(); controller = new AbortController(); refresh.disabled = true; error.textContent = "";
      try {
        const value = await api(path(), { signal: controller.signal }); if (epoch !== generation) return;
        note.textContent = "Asia/Shanghai · " + formatTime(value.since) + " 至 " + formatTime(value.until) + " · 指标保留 " + value.retentionDays + " 天" + (value.recordLimit ? "，最多 " + value.recordLimit + " 条明细" : "") + (value.limited ? " · 此范围历史不完整，升级起点 " + formatTime(value.retainedSince) : "") + (value.storage?.degraded || value.storage?.dropped ? " · 审计曾降级，记录可能缺失" : "") + "。积分为近 7 天本地估算，未计价部分不计入；密钥维度暂无积分归属。";
        const credits = new Map((value.credits || []).map(item => [item.name, item])); body.replaceChildren();
        for (const row of value.rows || []) {
          const tr = element("tr"), credit = credits.get(row.name);
          for (const cell of [row.name, row.requests, row.successRate.toFixed(1) + "%", row.averageMs + " ms", row.p95Ms + " ms", credit ? credit.estimated + (credit.unknown ? "（另 " + credit.unknown + " 次未计价）" : "") : "--"]) tr.append(element("td", "", String(cell)));
          body.append(tr);
        }
        if (!body.children.length) { const tr = element("tr"), td = element("td", "", "此范围暂无已结束请求"); td.colSpan = 6; tr.append(td); body.append(tr); }
      } catch (failure) { if (epoch === generation && failure.name !== "AbortError") error.textContent = failure.message + "；保留上次数据。"; }
      finally { if (epoch === generation) refresh.disabled = false; }
    }
    download.addEventListener("click", async () => {
      const epoch = generation; download.disabled = true;
      try { const text = await api(path() + "&format=csv", { responseType: "text" }); if (epoch !== generation) return; const url = URL.createObjectURL(new Blob(["\ufeff", text], { type: "text/csv;charset=utf-8" })); const a = element("a"); a.href = url; a.download = "request-report.csv"; a.click(); window.setTimeout(() => URL.revokeObjectURL(url), 1000); }
      catch (failure) { if (epoch === generation) error.textContent = failure.message; }
      finally { if (epoch === generation) download.disabled = false; }
    });
    range.addEventListener("change", load); dimension.addEventListener("change", load); refresh.addEventListener("click", load);
    return { load, clear() { generation++; controller?.abort(); body.replaceChildren(); note.textContent = error.textContent = ""; refresh.disabled = download.disabled = false; range.value = "today"; dimension.value = "day"; } };
  };
})();
