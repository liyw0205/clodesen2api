(() => {
  "use strict";
  window.createOperations = ({ api, element, formatTime }) => {
    const section = element("section", "settings-section"); section.id = "operations-panel";
    const heading = element("div", "section-heading");
    const copy = element("div"); copy.append(element("h2", "", "运行状态"));
    const refresh = element("button", "button button-secondary", "刷新状态"); refresh.type = "button"; refresh.id = "operations-refresh";
    heading.append(copy, refresh);
    const checked = element("p", "field-note"); checked.id = "operations-checked";
    const error = element("p", "form-error"); error.id = "operations-error"; error.setAttribute("role", "alert");
    const fields = element("dl", "operations-fields"); fields.id = "operations-fields";
    const eventsTitle = element("h3", "", "近期登录活动");
    const eventError = element("p", "form-error"); eventError.id = "login-events-error"; eventError.setAttribute("role", "alert");
    const events = element("ul", "login-events"); events.id = "login-events";
    section.append(heading, checked, error, fields, eventsTitle, eventError, events);
    document.querySelector("#settings-view .page-heading").after(section);
    let generation = 0, timer = 0, running = false, revision = 0;
    const indicator = document.querySelector("#service-state");
    function pair(label, value) { fields.append(element("dt", "", label), element("dd", "", value)); }
    function display(value) {
      fields.replaceChildren();
      pair("就绪状态", value.draining ? "恢复准备中" : value.ready ? "可接收请求" : "暂不可接收请求");
      pair("控制数据库", value.database ? "正常" : "不可用");
      pair("请求准入", value.admission ? "正常 · 在途 " + value.active : "不可用或未启用");
      pair("上游账号", "可用 " + value.accounts.available + " / 启用 " + value.accounts.enabled + " / 总计 " + value.accounts.total + " · 冷却 " + value.accounts.cooling);
      pair("任务队列", value.jobsAvailable ? "等待 " + value.queuedJobs + " · 运行 " + value.runningJobs + " · 历史失败 " + value.failedJobs : "不可用或未启用");
      pair("请求审计", (value.audit.degraded ? "存储降级" : "正常") + " · 排队 " + value.audit.queueLength + " · 丢弃 " + value.audit.dropped + " · 写入失败 " + value.audit.failures);
      pair("服务启动", formatTime(value.startedAt));
      checked.textContent = "最近检查 " + formatTime(value.checkedAt);
      const degraded = !value.ready || value.audit.degraded || !value.jobsAvailable;
      indicator.dataset.state = degraded ? "offline" : "online";
      indicator.querySelector("span").textContent = !value.ready ? "服务未就绪" : degraded ? "部分功能异常" : "服务就绪";
      indicator.title = indicator.querySelector("span").textContent;
    }
    async function load() {
      if (!running) return;
      const epoch = generation, request = ++revision;
      window.clearTimeout(timer); refresh.disabled = true;
      const statusTask = api("/admin/api/system-status").then(value => {
        if (epoch !== generation || request !== revision) return;
        error.textContent = ""; display(value);
      }).catch(failure => {
        if (epoch !== generation || request !== revision) return;
        error.textContent = "状态读取失败：" + failure.message;
        indicator.dataset.state = "offline"; indicator.querySelector("span").textContent = "状态读取失败";
      });
      const eventsTask = section.closest("[hidden]") || document.hidden ? Promise.resolve() : api("/admin/api/login-events").then(value => {
        if (epoch !== generation || request !== revision) return;
        events.replaceChildren(); eventError.textContent = "";
        const labels = { success: "登录成功", failure: "令牌不正确", invalid_request: "请求无效", session_failed: "会话创建失败" };
        if (!value.events.length) events.append(element("li", "muted", "暂无登录活动"));
        for (const item of value.events.slice(0, 20)) {
          const row = element("li"); row.append(element("time", "", formatTime(item.time)), element("code", "", item.clientIP), element("span", "", labels[item.event] || item.event)); events.append(row);
        }
      }).catch(failure => { if (epoch === generation && request === revision) eventError.textContent = failure.message; });
      await Promise.allSettled([statusTask, eventsTask]);
      if (epoch === generation && request === revision) {
        refresh.disabled = false;
        timer = window.setTimeout(load, document.hidden ? 60000 : 15000);
      }
    }
    refresh.addEventListener("click", load);
    document.addEventListener("visibilitychange", () => { if (!document.hidden && running) void load(); });
    return { load, start() { if (!running) { running = true; void load(); } }, clear() {
      generation++; revision++; running = false; window.clearTimeout(timer); timer = 0;
      fields.replaceChildren(); events.replaceChildren(); checked.textContent = ""; error.textContent = ""; eventError.textContent = ""; refresh.disabled = false;
      indicator.dataset.state = ""; indicator.querySelector("span").textContent = "未登录"; indicator.removeAttribute("title");
    } };
  };
})();
