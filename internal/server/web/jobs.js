(() => {
  "use strict";
  window.createJobs = ({ api, element, formatTime, showToast, onComplete }) => {
    const section = element("section", "accounts-section"); section.id = "jobs-panel";
    const heading = element("div", "section-heading"); heading.append(element("h2", "", "后台任务"));
    const refresh = element("button", "button button-secondary", "刷新任务"); refresh.type = "button"; heading.append(refresh);
    const probe = element("button", "button button-secondary", "探测连接"); probe.type = "button"; heading.append(probe);
    const error = element("p", "form-error"); error.setAttribute("role", "alert");
    const list = element("div", "jobs-list"); section.append(heading, error, list);
    const more = element("button", "button button-secondary", "显示更多任务"); more.type = "button"; more.hidden = true; section.append(more);
    document.querySelector("#accounts-view").append(section);
    let generation = 0, timer = null, loading = false, previous = new Map(), visible = 50;
    const active = job => ["queued", "running"].includes(job.state);
    const states = { queued: "排队中", running: "进行中", succeeded: "已完成", partial: "部分完成", failed: "失败", cancelled: "已取消", interrupted: "已中断" };
    const kinds = { checkin: "签到", redeem: "兑换", refresh: "刷新账号", probe: "探测连接" };
    const run = async (id, command) => {
      const epoch = generation;
      try { await api("/admin/api/jobs/" + encodeURIComponent(id) + "/" + command, { method: "POST" }); if (epoch === generation) await load(); }
      catch (failure) { if (epoch === generation) error.textContent = failure.message; }
    };
    async function load() {
      if (loading) return;
      loading = true; const epoch = generation; window.clearTimeout(timer); timer = null;
      try {
        const result = await api("/admin/api/jobs"); if (epoch !== generation) return;
        list.replaceChildren(); error.textContent = "";
        const jobs = result.jobs || [];
        if (!jobs.length) list.append(element("p", "field-note", "暂无后台任务"));
        let completed = false;
        more.hidden = jobs.length <= visible;
        for (const job of jobs.slice(0, visible)) {
          if (previous.get(job.id) && active({ state: previous.get(job.id) }) && !active(job)) completed = true;
          const row = element("details", "job-row"); row.dataset.jobId = job.id;
          const done = job.items.filter(item => !["queued", "running"].includes(item.state)).length;
          row.append(element("summary", "", (kinds[job.kind] || job.kind) + " · " + (states[job.state] || job.state) + " · " + done + "/" + job.items.length + " · " + formatTime(job.createdAt)));
          const progress = element("progress"); progress.max = Math.max(1, job.items.length); progress.value = done; progress.setAttribute("aria-label", "任务进度"); row.append(progress);
          for (const item of job.items) {
            let detail = "";
            if (item.result?.modelCount !== undefined) detail = " · " + item.result.modelCount + " 个模型";
            if (item.result?.balance !== undefined) detail = " · 余额 " + item.result.balance;
            row.append(element("p", "field-note", item.target + " · " + (states[item.state] || item.state) + detail + (item.error ? " · " + item.error : "")));
          }
          if (active(job) || (job.kind !== "redeem" && job.state !== "succeeded")) {
            const command = active(job) ? "cancel" : "retry";
            const button = element("button", "button button-secondary", command === "cancel" ? "取消任务" : "重试未完成项"); button.type = "button";
            button.disabled = job.cancelRequested; button.addEventListener("click", () => run(job.id, command)); row.append(button);
          }
          list.append(row);
        }
        previous = new Map(jobs.map(job => [job.id, job.state]));
        if (completed) onComplete();
        if (jobs.some(active)) timer = window.setTimeout(load, 1500);
      } catch (failure) { if (epoch === generation) error.textContent = failure.message; }
      finally { if (epoch === generation) loading = false; }
    }
    async function submit(kind, accountIds = [], code = "") {
      const epoch = generation;
      const result = await api("/admin/api/jobs", { method: "POST", body: JSON.stringify({ kind, accountIds, code }) });
      if (epoch !== generation) return;
      showToast(result.duplicate ? "任务已在队列中" : "任务已创建"); await load(); section.scrollIntoView({ block: "nearest" });
    }
    refresh.addEventListener("click", load);
    more.addEventListener("click", () => { visible += 50; void load(); });
    probe.addEventListener("click", async () => { const epoch = generation; probe.disabled = true; try { await submit("probe"); } catch (failure) { if (epoch === generation) error.textContent = failure.message; } finally { if (epoch === generation) probe.disabled = false; } });
    return { load, submit, clear() { generation++; window.clearTimeout(timer); timer = null; loading = false; probe.disabled = false; visible = 50; more.hidden = true; previous.clear(); list.replaceChildren(); error.textContent = ""; } };
  };
})();
