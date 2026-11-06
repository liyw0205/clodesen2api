(() => {
  "use strict";
  window.createModelPolicies = ({ api, element, showToast, render }) => {
    const $ = selector => document.querySelector(selector);
    const dialog = $("#model-policy-editor");
    let snapshot = { revision: 0, models: [] };
    let editing = null;
    let generation = 0;
    let busy = false;
    function accept(value) {
      if (!Number.isInteger(value?.revision) || !Array.isArray(value.models)) throw new Error("模型策略响应无效");
      snapshot = value;
      $("#model-policy-revision").textContent = "策略版本 " + value.revision;
    }
    async function load() {
      const current = generation;
      const result = await api("/admin/api/model-policies");
      if (current !== generation) return;
      accept(result);
    }
    function rule(id) { return snapshot.models.find(item => item.sourceId === id); }
    function controls(id) {
      const wrap = element("div", "key-actions");
      const item = rule(id);
      const state = element("span", "status-badge " + (item?.enabled === false ? "status-paused" : "status-ready"), item?.enabled === false ? "已停用" : "启用");
      const edit = element("button", "row-action", "编辑策略");
      edit.type = "button";
      edit.disabled = busy || !item;
      edit.addEventListener("click", () => open(item));
      wrap.append(state, edit);
      return wrap;
    }
    function open(item) {
      if (busy || !item) return;
      editing = { ...item, revision: snapshot.revision };
      $("#model-policy-form").reset();
      $("#model-policy-source").textContent = item.sourceId;
      $("#model-policy-public").value = item.publicId;
      $("#model-policy-enabled").checked = item.enabled;
      $("#model-policy-original").checked = item.keepOriginal;
      $("#model-policy-upstream").replaceChildren(...snapshot.models.map(model => {
        const option = element("option", "", model.sourceId);
        option.value = model.sourceId;
        return option;
      }));
      $("#model-policy-upstream").value = item.upstreamId;
      $("#model-policy-endpoints").value = item.endpoints?.length ? "known" : "unknown";
      for (const input of document.querySelectorAll("[name=model-endpoint]")) input.checked = item.endpoints?.includes(input.value) || false;
      $("#model-policy-stream").value = item.stream === null || item.stream === undefined ? "unknown" : String(item.stream);
      $("#model-policy-meter").value = item.metering?.source || "";
      $("#model-policy-token-bound").value = item.metering?.tokenUpperBound || "";
      $("#model-policy-credit-bound").value = item.metering?.creditUpperBound || "";
      $("#model-policy-error").textContent = "";
      updateCapabilities();
      dialog.showModal();
    }
    function updateCapabilities() {
      $("#model-policy-capabilities").hidden = $("#model-policy-endpoints").value !== "known";
    }
    async function save(reset = false) {
      if (busy || !editing) return;
      const current = generation;
      const item = editing;
      const endpoints = $("#model-policy-endpoints").value === "known" ? Array.from(document.querySelectorAll("[name=model-endpoint]:checked"), input => input.value) : [];
      if (!reset && $("#model-policy-endpoints").value === "known" && !endpoints.length) { $("#model-policy-error").textContent = "至少选择一个接口"; return; }
      const stream = $("#model-policy-stream").value;
      const payload = reset ? { revision: item.revision } : {
        revision: item.revision,
        rule: { sourceId: item.sourceId, publicId: $("#model-policy-public").value.trim(), upstreamId: $("#model-policy-upstream").value, enabled: $("#model-policy-enabled").checked, keepOriginal: $("#model-policy-original").checked, endpoints, stream: stream === "unknown" ? null : stream === "true" },
      };
      if (!reset) payload.rule.metering = { source: $("#model-policy-meter").value, tokenUpperBound: Number($("#model-policy-token-bound").value || 0), creditUpperBound: Number($("#model-policy-credit-bound").value || 0) };
      busy = true;
      $("#model-policy-save").disabled = true;
      $("#model-policy-reset").disabled = true;
      $("#model-policy-error").textContent = "";
      try {
        const result = await api("/admin/api/model-policies/" + encodeURIComponent(item.sourceId), { method: reset ? "DELETE" : "PUT", body: JSON.stringify(payload) });
        if (current !== generation) return;
        accept(result);
        dialog.close();
        render();
        showToast(reset ? "模型策略已重置" : "模型策略已保存");
      } catch (error) {
        if (current !== generation) return;
        if (error.status === 409) {
          await load();
          if (current !== generation) return;
          render();
        }
        $("#model-policy-error").textContent = error.message;
      } finally {
        if (current === generation) {
          busy = false;
          $("#model-policy-save").disabled = false;
          $("#model-policy-reset").disabled = false;
          render();
        }
      }
    }
    $("#model-policy-form").addEventListener("submit", event => { event.preventDefault(); void save(); });
    $("#model-policy-reset").addEventListener("click", () => { void save(true); });
    $("#model-policy-endpoints").addEventListener("change", updateCapabilities);
    return { load, rule, controls, clear() {
      generation++;
      snapshot = { revision: 0, models: [] };
      editing = null;
      busy = false;
      dialog.close();
      $("#model-policy-form").reset();
      $("#model-policy-save").disabled = false;
      $("#model-policy-reset").disabled = false;
    } };
  };
})();
