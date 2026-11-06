(() => {
  "use strict";
  window.createAdmission = ({ api, element }) => {
    const section = element("section", "settings-section");
    section.append(element("h2", "", "请求准入"));
    const form = element("form", "settings-form");
    const grid = element("div", "settings-grid");
    const fields = [
      ["maxConcurrent", "全局并发上限", 1, 10000], ["modelConcurrent", "每模型并发上限", 1, 10000],
      ["bodyBytes", "在途请求体预算（字节）", 1048576, 8589934592], ["bodyTimeoutSeconds", "请求体读取超时（秒）", 1, 600],
      ["firstByteTimeoutSeconds", "首字节超时（秒）", 1, 3600], ["idleTimeoutSeconds", "响应空闲超时（秒）", 1, 3600],
      ["requestTimeoutSeconds", "整体超时（秒）", 1, 86400], ["maxAttempts", "上游请求尝试上限", 1, 32], ["timezone", "配额时区"]
    ];
    const inputs = new Map();
    for (const [name, text, min, max] of fields) {
      const label = element("label", "", text);
      const input = element("input"); input.id = "admission-" + name; input.required = true;
      input.type = min === undefined ? "text" : "number";
      if (min !== undefined) { input.min = min; input.max = max; input.step = 1; }
      label.append(input); grid.append(label); inputs.set(name, input);
    }
    const status = element("p", "field-note");
    const error = element("p", "form-error"); error.setAttribute("role", "alert");
    const save = element("button", "button button-primary", "保存准入设置"); save.type = "submit";
    form.append(grid, status, error, save); section.append(form);
    document.querySelector("#settings-view").append(section);
    const pending = element("div", "metering-pending"); section.append(pending);
    let current = null, generation = 0, busy = false;
    const render = view => {
      current = view;
      for (const [name, input] of inputs) input.value = view.settings[name];
      status.textContent = "当前在途 " + view.active + " · 请求体预算占用 " + view.bodyBytes + " 字节 · 版本 " + view.revision;
      save.disabled = false;
      pending.replaceChildren();
      const reservations = Object.values(view.reservations || {}).filter(item => item.state === "unknown" || item.state === "overrun").sort((a,b) => new Date(b.updatedAt)-new Date(a.updatedAt));
      if (reservations.length) pending.append(element("h3", "", "用量待核对（" + reservations.length + "）"));
      for (const item of reservations.slice(0, 50)) {
        const row = element("form", "settings-form");
        row.append(element("code", "", item.id), element("p", "field-note", item.key + " · " + item.state + " · " + item.source));
        const fields = element("div", "key-form-grid");
        const amounts = {};
        for (const [unit, label] of [["tokens", "Token"], ["credits", "积分"]]) {
          const known = unit + "Known";
          if (!item.bound[known]) continue;
          const wrap = element("label", "", label + " · 预留 " + item.bound[unit]);
          const input = element("input"); input.type = "number"; input.min = 0; input.max = 1000000000000; input.step = 1; input.required = true;
          input.value = item.actual[known] ? item.actual[unit] : "";
          input.disabled = item.actual[known] || item.state !== "unknown";
          wrap.append(input); fields.append(wrap); amounts[unit] = input;
        }
        row.append(fields);
        if (item.state === "unknown") {
          const submit = element("button", "button button-secondary", "确认对账"); submit.type = "submit"; row.append(submit);
          row.addEventListener("submit", async event => {
            event.preventDefault(); const epoch = generation; submit.disabled = true;
            const actual = { ...item.actual };
            for (const [unit, input] of Object.entries(amounts)) { actual[unit] = Number(input.value); actual[unit + "Known"] = true; }
            try { await api("/admin/api/admission/reconcile", { method: "POST", body: JSON.stringify({ id: item.id, version: item.version, actual }) }); if (epoch === generation) await load(); }
            catch (failure) { if (epoch === generation) { error.textContent = failure.message; submit.disabled = false; } }
          });
        }
        pending.append(row);
      }
    };
    async function load() {
      const epoch = generation;
      try { const view = await api("/admin/api/admission"); if (epoch === generation) { render(view); error.textContent = ""; } }
      catch (failure) { if (epoch === generation) error.textContent = failure.message; }
    }
    form.addEventListener("submit", async event => {
      event.preventDefault(); if (busy || !current) return;
      const epoch = generation; busy = true; save.disabled = true; error.textContent = "";
      const settings = Object.fromEntries(Array.from(inputs, ([name, input]) => [name, input.type === "number" ? Number(input.value) : input.value.trim()]));
      try { const view = await api("/admin/api/admission", { method: "PUT", body: JSON.stringify({ revision: current.revision, settings }) }); if (epoch === generation) render(view); }
      catch (failure) { if (epoch === generation) { if (failure.status === 409) await load(); error.textContent = failure.message; } }
      finally { if (epoch === generation) { busy = false; save.disabled = false; } }
    });
    return { load, clear() { generation++; current = null; busy = false; save.disabled = true; form.reset(); pending.replaceChildren(); error.textContent = ""; status.textContent = ""; } };
  };
})();
