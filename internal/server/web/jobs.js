(() => {
  "use strict";
  window.createJobs = ({ api, showToast, onSubmitted, onUpdate, onComplete }) => {
    let generation = 0, timer = null, loading = false, previous = new Map();
    const submitted = new Map();
    const active = job => ["queued", "running"].includes(job.state);
    const kinds = { checkin: "签到", redeem: "兑换", refresh: "刷新账号", probe: "探测连接" };
    async function load() {
      if (loading) return;
      loading = true; const epoch = generation; window.clearTimeout(timer); timer = null;
      let poll = false;
      try {
        const result = await api("/admin/api/jobs"); if (epoch !== generation) return;
        const jobs = result.jobs || [];
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
        if (completed) onComplete(completedJobs);
      } catch (_) { poll = previous.size > 0; }
      finally {
        if (epoch === generation) {
          loading = false;
          if (poll || submitted.size) timer = window.setTimeout(load, 1500);
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
    return { load, submit, clear() { generation++; window.clearTimeout(timer); timer = null; loading = false; previous.clear(); submitted.clear(); } };
  };
})();
