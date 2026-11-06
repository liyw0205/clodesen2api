(() => {
  "use strict";
  window.createStorage = ({ api, element, formatTime }) => {
    const section = element("section", "settings-section"); section.id = "storage-panel";
    section.append(element("h2", "", "存储与备份"), element("p", "field-note", "完整备份包含账号凭据、密钥摘要、会话、设置和审计数据。"));
    const create = element("button", "button button-secondary", "创建完整备份"); create.type = "button";
    const label = element("label", "", "校验恢复备份");
    const upload = element("input"); upload.type = "file"; upload.accept = ".zip"; upload.id = "backup-upload"; label.append(upload);
    const error = element("p", "form-error"); error.setAttribute("role", "alert");
    const result = element("p", "field-note"); result.id = "backup-validation"; result.setAttribute("role", "status");
    const list = element("div", "jobs-list"); section.append(create, label, error, result, list);
    const restore = element("button", "button button-secondary", "恢复此备份并重启"); restore.type = "button"; restore.hidden = true; section.append(restore);
    document.querySelector("#settings-view").append(section);
    let generation = 0;
    let currentPlan = null;
    async function load() {
      const epoch = generation;
      try {
        const payload = await api("/admin/api/backups"); if (epoch !== generation) return;
        const status = await api("/admin/api/backups/restore-status"); if (epoch !== generation) return;
        if (status.state === "restored") result.textContent = "已恢复 · " + formatTime(status.verifiedAt);
        list.replaceChildren();
        if (!payload.backups.length) list.append(element("p", "field-note", "暂无备份"));
        for (const item of payload.backups) {
          const row = element("div", "job-row");
          row.append(element("span", "", formatTime(item.createdAt) + " · " + Math.ceil(item.size / 1024) + " KB · " + (item.automatic ? "自动备份" : "手动备份") + " "));
          const link = element("a", "text-button", "下载"); link.href = "/admin/api/backups/" + encodeURIComponent(item.id); link.download = item.id + ".zip"; row.append(link); list.append(row);
        }
      } catch (failure) { if (epoch === generation) error.textContent = failure.message; }
    }
    create.addEventListener("click", async () => {
      const epoch = generation; create.disabled = true; error.textContent = "";
      try { await api("/admin/api/backups", { method: "POST" }); if (epoch === generation) await load(); }
      catch (failure) { if (epoch === generation) error.textContent = failure.message; }
      finally { if (epoch === generation) create.disabled = false; }
    });
    upload.addEventListener("change", async () => {
      const file = upload.files[0]; if (!file) return;
      currentPlan = null; restore.hidden = true;
      const epoch = generation; upload.disabled = true; error.textContent = ""; result.textContent = "正在校验";
      try {
        const response = await fetch("/admin/api/backups/validate", { method: "POST", body: file, credentials: "same-origin", cache: "no-store" });
        if (response.status === 401) { await api("/admin/api/backups"); return; }
        const plan = await response.json(); if (epoch !== generation) return;
        if (!response.ok) throw new Error(plan.error || "校验失败");
        currentPlan = plan; restore.hidden = false;
        result.textContent = "已验证 · 版本 " + plan.manifest.version + " · " + plan.manifest.entries.length + " 个文件 · 有效至 " + formatTime(plan.expiresAt);
      } catch (failure) { if (epoch === generation) { result.textContent = ""; error.textContent = failure.message; } }
      finally { if (epoch === generation) { upload.disabled = false; upload.value = ""; } }
    });
    restore.addEventListener("click", async () => {
      if (!currentPlan || !window.confirm("恢复将替换当前数据并重启服务。已自动备份当前状态，是否继续？")) return;
      const epoch = generation; restore.disabled = true; error.textContent = "";
      try { await api("/admin/api/backups/restore", { method: "POST", body: JSON.stringify({ planId: currentPlan.id }) }); if (epoch === generation) { result.textContent = "服务正在恢复并重启，请重新连接。"; restore.hidden = true; currentPlan = null; } }
      catch (failure) { if (epoch === generation) error.textContent = failure.message; }
      finally { if (epoch === generation) restore.disabled = false; }
    });
    return { load, clear() { generation++; currentPlan = null; restore.hidden = true; restore.disabled = false; create.disabled = false; upload.disabled = false; upload.value = ""; list.replaceChildren(); error.textContent = ""; result.textContent = ""; } };
  };
})();
