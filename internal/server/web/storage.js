(() => {
  "use strict";
  window.createStorage = ({ api, element, formatTime }) => {
    const section = element("section", "settings-section storage-section"); section.id = "storage-panel";
    const heading = element("div", "storage-heading");
    const copy = element("div", "storage-heading-copy");
    copy.append(element("p", "section-kicker", "DATA SAFETY"), element("h2", "", "存储与备份"), element("p", "muted", "完整备份包含账号凭据、密钥摘要、会话、设置和审计数据。"));
    heading.append(copy);
    const actions = element("div", "storage-actions");
    const create = element("button", "button button-primary", "创建完整备份"); create.type = "button";
    const label = element("label", "backup-upload-control");
    const uploadText = element("span", "button button-secondary", "校验恢复备份"); uploadText.setAttribute("aria-hidden", "true");
    const upload = element("input"); upload.type = "file"; upload.accept = ".zip"; upload.id = "backup-upload"; upload.setAttribute("aria-label", "选择备份文件"); label.append(uploadText, upload);
    actions.append(create, label);
    const error = element("p", "form-error"); error.setAttribute("role", "alert");
    const result = element("p", "field-note backup-status"); result.id = "backup-validation"; result.setAttribute("role", "status");
    const list = element("div", "backup-list");
    section.append(heading, actions, error, result, list);
    const restore = element("button", "button button-secondary backup-restore", "恢复此备份并重启"); restore.type = "button"; restore.hidden = true; section.append(restore);
    document.querySelector("#settings-view").append(section);
    const schedule = window.createBackupSchedule({ api, element, formatTime, parent: section });
    let generation = 0;
    let currentPlan = null;
    async function load() {
      void schedule.load();
      const epoch = generation;
      try {
        const payload = await api("/admin/api/backups"); if (epoch !== generation) return;
        const status = await api("/admin/api/backups/restore-status"); if (epoch !== generation) return;
        if (status.state === "restored") result.textContent = "已恢复 · " + formatTime(status.verifiedAt);
        list.replaceChildren();
        if (!payload.backups.length) list.append(element("p", "field-note backup-empty", "暂无备份"));
        for (const item of payload.backups) {
          const row = element("article", "backup-item");
          const details = element("div", "backup-item-details");
          const title = element("div", "backup-item-title");
          title.append(element("strong", "", item.automatic ? "自动备份" : "手动备份"), element("span", "backup-kind", item.automatic ? "定时保留" : "可手动删除"));
          details.append(title, element("span", "backup-item-meta", formatTime(item.createdAt) + " · " + Math.ceil(item.size / 1024) + " KB"));
          const itemActions = element("div", "backup-item-actions");
          const link = element("a", "button button-quiet", "下载"); link.href = "/admin/api/backups/" + encodeURIComponent(item.id); link.download = item.id + ".zip";
          const remove = element("button", "button button-quiet backup-delete", "删除"); remove.type = "button"; remove.setAttribute("aria-label", "删除" + (item.automatic ? "自动" : "手动") + "备份");
          remove.addEventListener("click", async () => {
            if (!window.confirm("删除这份备份？删除后无法恢复。")) return;
            const epoch = generation; remove.disabled = true; error.textContent = "";
            try {
              await api("/admin/api/backups/" + encodeURIComponent(item.id), { method: "DELETE" });
              if (epoch === generation) { result.textContent = "备份已删除"; await load(); }
            } catch (failure) { if (epoch === generation) error.textContent = failure.message; }
            finally { remove.disabled = false; }
          });
          itemActions.append(link, remove); row.append(details, itemActions); list.append(row);
        }
      } catch (failure) { if (epoch === generation) error.textContent = failure.message; }
    }
    create.addEventListener("click", async () => {
      const epoch = generation; create.disabled = true; error.textContent = "";
      try { await api("/admin/api/backups", { method: "POST", timeout: 120000 }); if (epoch === generation) await load(); }
      catch (failure) { if (epoch === generation) error.textContent = failure.message; }
      finally { if (epoch === generation) create.disabled = false; }
    });
    upload.addEventListener("change", async () => {
      const file = upload.files[0]; if (!file) return;
      currentPlan = null; restore.hidden = true;
      const epoch = generation; upload.disabled = true; error.textContent = ""; result.textContent = "正在校验";
      try {
        const plan = await api("/admin/api/backups/validate", { method: "POST", body: file, timeout: 120000 }); if (epoch !== generation) return;
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
    return { load, clear() { generation++; schedule.clear(); currentPlan = null; restore.hidden = true; restore.disabled = false; create.disabled = false; upload.disabled = false; upload.value = ""; list.replaceChildren(); error.textContent = ""; result.textContent = ""; } };
  };
})();
