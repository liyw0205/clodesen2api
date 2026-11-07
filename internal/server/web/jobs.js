(() => {
  "use strict";
  window.createJobs = ({ api, element, formatTime, accountName, showToast, onSubmitted, onUpdate, onComplete }) => {
    const $ = selector => document.querySelector(selector);
    const dialog = $("#jobs-dialog");
    let generation = 0, timer = null, loading = false, previous = new Map(), jobs = [], failures = 0, busy = false, rendered = "";
    const submitted = new Map();
    const active = job => ["queued", "running"].includes(job.state);
    const kinds = { checkin: "签到", redeem: "兑换", refresh: "刷新账号", probe: "探测连接" };
    const states = { queued: "排队中", running: "执行中", succeeded: "已完成", partial: "部分成功", failed: "失败", cancelled: "已取消", interrupted: "已中断" };
    function render() {
      const count = jobs.filter(job => active(job)).length;
      $("#jobs-open").textContent = count ? "任务 " + count : "任务";
      $("#jobs-summary").textContent = jobs.length + " 个任务 · " + count + " 个进行中";
      const fingerprint = JSON.stringify([jobs, $("#jobs-kind").value, $("#jobs-state").value, busy, jobs.flatMap(job => job.items.map(item => accountName(item.target)))]);
      if (rendered === fingerprint) return;
      rendered = fingerprint;
      const list = $("#jobs-list");
      const expanded = new Set(Array.from(list.querySelectorAll("details[open]"), node => node.dataset.jobId));
      list.replaceChildren();
      const visible = jobs.filter(job => (!$("#jobs-kind").value || job.kind === $("#jobs-kind").value) && (!$("#jobs-state").value || ($("#jobs-state").value === "active" ? active(job) : !active(job))));
      $("#jobs-empty").hidden = visible.length > 0;
      for (const job of visible) {
        const details = element("details", "job-item"); details.dataset.jobId = job.id; details.open = expanded.has(job.id);
        const summary = element("summary", "job-summary");
        const finished = job.items.filter(item => !active(item)).length;
        summary.append(element("strong", "", kinds[job.kind] || job.kind), element("span", "status-badge " + (job.state === "succeeded" ? "status-ready" : job.state === "failed" || job.state === "interrupted" ? "status-error" : ""), states[job.state] || job.state), element("span", "field-note", finished + " / " + job.items.length), element("time", "field-note", formatTime(job.createdAt)));
        const progress = element("progress"); progress.max = job.items.length; progress.value = finished; progress.setAttribute("aria-label", (kinds[job.kind] || job.kind) + "任务进度");
        const commands = element("div", "job-commands"); commands.append(element("code", "", job.id));
        const operation = active(job) ? "cancel" : job.kind !== "redeem" && job.items.some(item => item.state !== "succeeded") ? "retry" : "";
        if (operation) {
          const button = element("button", "button button-secondary", operation === "cancel" ? job.cancelRequested ? "正在取消" : "取消任务" : "重试未完成项"); button.type = "button";
          button.disabled = busy || job.cancelRequested;
          button.addEventListener("click", () => { void act(job, operation); }); commands.append(button);
        }
        const items = element("ul", "job-results");
        for (const item of job.items) {
          const row = element("li");
          row.append(element("strong", "", accountName(item.target)), element("span", "", states[item.state] || item.state));
          const message = item.result?.message || item.error;
          if (message) row.append(element("p", "field-note", message));
          if (item.result && typeof item.result === "object") {
            const labels = { checkedIn: "已签到", alreadyChecked: "此前已签到", reward: "奖励积分", balance: "余额", credits: "兑换积分", modelCount: "模型数", ok: "成功" };
            const values = Object.entries(item.result).filter(([key, value]) => labels[key] && value != null && typeof value !== "object");
            if (values.length) row.append(element("p", "field-note", values.map(([key, value]) => labels[key] + "：" + (typeof value === "boolean" ? value ? "是" : "否" : value)).join(" · ")));
          }
          items.append(row);
        }
        details.append(summary, progress, commands, items); list.append(details);
      }
    }
    async function act(job, operation) {
      if (busy) return;
      const epoch = generation;
      busy = true; $("#jobs-error").textContent = ""; render();
      try {
        const result = await api("/admin/api/jobs/" + encodeURIComponent(job.id) + "/" + operation, { method: "POST" });
        if (epoch !== generation) return;
        if (result?.job) submitted.set(result.job.id, result.job.kind);
        showToast(operation === "cancel" ? "已请求取消任务" : "已提交未完成项重试");
        await load();
      } catch (error) { if (epoch === generation) $("#jobs-error").textContent = error.message; }
      finally { if (epoch === generation) { busy = false; render(); } }
    }
    async function load() {
      if (loading) return;
      loading = true; const epoch = generation; window.clearTimeout(timer); timer = null;
      let poll = false;
      try {
        const result = await api("/admin/api/jobs"); if (epoch !== generation) return;
        jobs = result.jobs || [];
        failures = 0;
        $("#jobs-error").textContent = "";
        let completed = false;
        const completedJobs = [];
        for (const job of jobs) {
          if (submitted.has(job.id) && onUpdate) onUpdate(job);
          if (active(job)) { poll = true; continue; }
          if (previous.get(job.id) || submitted.has(job.id)) completed = true;
          if (submitted.has(job.id)) {
            const name = kinds[job.kind] || "操作";
            completedJobs.push(job);
            if (job.kind !== "redeem") {
              showToast(job.state === "succeeded" ? name + "已完成" : name + "未全部完成，请查看账号状态");
            }
            submitted.delete(job.id);
          }
        }
        previous = new Map(jobs.filter(active).map(job => [job.id, true]));
        render();
        if (completed) onComplete(completedJobs);
      } catch (error) {
        if (epoch !== generation) return;
        failures++;
        $("#jobs-error").textContent = "任务状态读取失败：" + error.message;
        poll = previous.size > 0 || dialog.open;
      }
      finally {
        if (epoch === generation) {
          loading = false;
          if (poll || submitted.size || dialog.open) timer = window.setTimeout(load, Math.min(30000, (document.hidden ? 15000 : 1500) * 2 ** Math.min(failures, 4)));
        }
      }
    }
    async function submit(kind, accountIds = [], code = "") {
      const epoch = generation;
      const result = await api("/admin/api/jobs", { method: "POST", body: JSON.stringify({ kind, accountIds, code }) });
      if (epoch !== generation) return;
      submitted.set(result.job.id, kind);
      if (onSubmitted) onSubmitted(result.job);
      if (kind !== "redeem") showToast((kinds[kind] || "操作") + "已提交");
      await load();
      return result.job;
    }
    $("#jobs-open").addEventListener("click", () => { dialog.showModal(); render(); void load(); });
    $("#jobs-refresh").addEventListener("click", () => { void load(); });
    $("#jobs-kind").addEventListener("change", render);
    $("#jobs-state").addEventListener("change", render);
    document.addEventListener("visibilitychange", () => { if (!document.hidden && (previous.size || dialog.open)) void load(); });
    return { load, submit, clear() { generation++; window.clearTimeout(timer); timer = null; loading = false; previous.clear(); submitted.clear(); jobs = []; busy = false; failures = 0; dialog.close(); $("#jobs-error").textContent = ""; render(); } };
  };
})();
