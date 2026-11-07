(() => {
  "use strict";
  window.createSettings = ({ $, api, state, element, settingsTools, renderUpdateSourceOptions, showToast }) => {
    function setSettingsForm(settings) {
      state.settings = settings;
      $("#settings-server-addr").value = settings.server?.addr || "";
      $("#settings-base-url").value = settings.upstream?.baseURL || "";
      $("#settings-openai-url").value = settings.upstream?.openAIBaseURL || "";
      $("#settings-login-path").value = settings.upstream?.loginPath || "";
      $("#settings-login-timeout").value = settings.upstream?.loginTimeout || "30s";
      $("#settings-request-timeout").value = settings.upstream?.requestTimeout || "0s";
      $("#settings-user-agent").value = settings.upstream?.userAgent || "";
      $("#settings-strategy").value = settings.auth?.strategy || "fixed";
      $("#settings-account-rpm").value = settings.auth?.rpm || 4;
      $("#settings-auto-relogin").checked = Boolean(settings.auth?.autoRelogin);
      $("#settings-checkin-enable").checked = Boolean(settings.checkin?.enable);
      $("#settings-checkin-path").value = settings.checkin?.path || "";
      $("#settings-checkin-at").value = settings.checkin?.at || "04:00";
      $("#settings-checkin-timezone").value = settings.checkin?.timezone || "Asia/Shanghai";
      $("#update-custom-prefix").value = settings.update?.customPrefix || "";
      state.updateSourceID = settings.update?.source || "builtin:gh-proxy-com";
      if (state.updateSourceOptions.length) renderUpdateSourceOptions(state.updateSourceOptions, state.updateSourceID);
      else $("#update-custom-wrap").hidden = state.updateSourceID !== "custom";
      setRedemptionSettingsForm(settings);
      settingsTools.set(settings);
      renderSettingsSources(settings);
    }

    function renderSettingsSources(settings) {
      const controls = {
        "server.addr": "settings-server-addr", "upstream.baseURL": "settings-base-url", "upstream.openAIBaseURL": "settings-openai-url",
        "upstream.loginPath": "settings-login-path", "upstream.loginTimeout": "settings-login-timeout", "upstream.requestTimeout": "settings-request-timeout", "upstream.userAgent": "settings-user-agent",
        "auth.strategy": "settings-strategy", "auth.rpm": "settings-account-rpm", "auth.autoRelogin": "settings-auto-relogin",
        "checkin.enable": "settings-checkin-enable", "checkin.path": "settings-checkin-path", "checkin.at": "settings-checkin-at", "checkin.timezone": "settings-checkin-timezone",
        "cache.accountsSeconds": "settings-cache-accountsSeconds", "cache.modelsSeconds": "settings-cache-modelsSeconds", "cache.creditsSeconds": "settings-cache-creditsSeconds",
        "redemption.enable": "redemption-auto-enable", "redemption.at": "redemption-auto-at", "redemption.code": "redemption-auto-code",
      };
      const sources = { cli: "命令行", environment: "环境变量", management: "管理设置", file: "配置文件", default: "默认值" };
      for (const [path, id] of Object.entries(controls)) {
        const input = document.getElementById(id); const field = settings.fields?.[path];
        if (!input) continue;
        input.disabled = Boolean(field?.locked);
        const parent = input.closest("label"); if (!parent) continue;
        parent.querySelector(".setting-source")?.remove();
        if (field) parent.append(element("span", "field-note setting-source", sources[field.source] + (field.locked ? " · 已锁定" : field.pending ? " · 待重启" : field.mode === "restart" ? " · 重启生效" : " · 即时生效")));
      }
    }

    function setRedemptionSettingsForm(settings) {
      state.settings = settings;
      $("#redemption-auto-enable").checked = Boolean(settings?.redemption?.enable);
      $("#redemption-auto-at").value = settings?.redemption?.at || "04:30";
      $("#redemption-auto-code").value = settings?.redemption?.code || "";
      $("#redemption-auto-note").textContent = settings?.redemption?.codeConfigured ? "自动兑换码已配置。" : "尚未配置兑换码。";
      $("#redemption-auto-error").textContent = "";
    }

    async function loadSettings() {
      const epoch = state.sessionGeneration;
      try { const settings = await api("/admin/api/config"); if (epoch !== state.sessionGeneration) return; setSettingsForm(settings); $("#settings-error").textContent = ""; }
      catch (error) { if (epoch === state.sessionGeneration) $("#settings-error").textContent = error.message; }
    }

    function draftChanges(fields) {
      const draft = {};
      for (const [path, id] of Object.entries(fields)) {
        const input = document.getElementById(id), [group, name] = path.split(".");
        const value = input.type === "checkbox" ? input.checked : input.type === "number" ? Number(input.value) : input.value.trim();
        if (!input.disabled && value !== state.settings?.[group]?.[name]) draft[id] = value;
      }
      return draft;
    }

    async function reloadConflict(draft, epoch) {
      const settings = await api("/admin/api/config");
      if (epoch !== state.sessionGeneration) return;
      setSettingsForm(settings);
      for (const [id, value] of Object.entries(draft)) {
        const input = document.getElementById(id);
        if (input.disabled) continue;
        if (input.type === "checkbox") input.checked = value;
        else input.value = value;
      }
      $("#settings-form").dispatchEvent(new Event("input", { bubbles: true }));
    }

    async function saveSettings(event) {
      event.preventDefault();
      const button = $("#settings-save");
      button.disabled = true;
      $("#settings-error").textContent = "";
      const epoch = state.sessionGeneration;
      const draft = draftChanges(settingsTools.fields);
      const payload = { ...state.settings, cache: settingsTools.cache(), server: { addr: $("#settings-server-addr").value.trim() }, upstream: {
        baseURL: $("#settings-base-url").value.trim(), openAIBaseURL: $("#settings-openai-url").value.trim(),
        loginPath: $("#settings-login-path").value.trim(), loginTimeout: $("#settings-login-timeout").value.trim(),
        requestTimeout: $("#settings-request-timeout").value.trim(), userAgent: $("#settings-user-agent").value.trim(),
      }, auth: { strategy: $("#settings-strategy").value, rpm: Number($("#settings-account-rpm").value), autoRelogin: $("#settings-auto-relogin").checked }, checkin: {
        enable: $("#settings-checkin-enable").checked, path: $("#settings-checkin-path").value.trim(),
        at: $("#settings-checkin-at").value, timezone: $("#settings-checkin-timezone").value.trim(),
      }, redemption: {
        enable: Boolean(state.settings?.redemption?.enable), at: state.settings?.redemption?.at || "04:30",
        code: state.settings?.redemption?.code || "",
      }};
      try { const result = await api("/admin/api/config", { method: "PUT", body: JSON.stringify(payload) }); if (epoch !== state.sessionGeneration) return; setSettingsForm(result.settings); showToast(result.restartRequired ? "设置已保存，部分设置需重启生效" : "设置已保存"); }
      catch (error) {
        if (epoch !== state.sessionGeneration) return;
        if (error.status === 409) {
          try {
            await reloadConflict(draft, epoch);
            if (epoch === state.sessionGeneration) $("#settings-error").textContent = "配置已更新，已读取最新配置并保留未锁定字段的修改，请核对后再次保存。";
          } catch (failure) {
            if (epoch === state.sessionGeneration) $("#settings-error").textContent = error.message + "；重新读取失败：" + failure.message;
          }
        } else $("#settings-error").textContent = error.message;
      }
      finally { if (epoch === state.sessionGeneration) button.disabled = false; }
    }

    async function loadRedemptionSettings() {
      const epoch = state.sessionGeneration;
      try {
        const settings = await api("/admin/api/config");
        if (epoch !== state.sessionGeneration) return;
        setRedemptionSettingsForm(settings);
        renderSettingsSources(settings);
      } catch (error) {
        if (epoch === state.sessionGeneration && error.message !== "登录状态已过期，请重新登录") $("#redemption-auto-error").textContent = error.message;
      }
    }

    async function saveRedemptionSettings() {
      const epoch = state.sessionGeneration;
      const button = $("#redemption-settings-save");
      button.disabled = true;
      $("#redemption-auto-error").textContent = "";
      const draft = draftChanges({ "redemption.enable": "redemption-auto-enable", "redemption.at": "redemption-auto-at", "redemption.code": "redemption-auto-code" });
      try {
        const current = state.settings || await api("/admin/api/config");
        if (epoch !== state.sessionGeneration) return;
        const payload = JSON.parse(JSON.stringify(current));
        payload.redemption = {
          enable: $("#redemption-auto-enable").checked,
          at: $("#redemption-auto-at").value,
          code: $("#redemption-auto-code").value.trim(),
        };
        const result = await api("/admin/api/config", { method: "PUT", body: JSON.stringify(payload) });
        if (epoch !== state.sessionGeneration) return;
        setRedemptionSettingsForm(result.settings);
        renderSettingsSources(result.settings);
        showToast(result.restartRequired ? "自动兑换设置已保存，重启后完整生效" : "自动兑换设置已保存");
      } catch (error) {
        if (epoch !== state.sessionGeneration) return;
        if (error.status === 409) {
          try {
            await reloadConflict(draft, epoch);
            if (epoch === state.sessionGeneration) $("#redemption-auto-error").textContent = "配置已更新，已读取最新配置并保留未锁定字段的修改，请核对后再次保存。";
          } catch (failure) {
            if (epoch === state.sessionGeneration) $("#redemption-auto-error").textContent = error.message + "；重新读取失败：" + failure.message;
          }
        } else $("#redemption-auto-error").textContent = error.message;
      } finally {
        if (epoch === state.sessionGeneration) button.disabled = false;
      }
    }

    function bind() {
      $("#settings-refresh").addEventListener("click", loadSettings);
      $("#settings-form").addEventListener("submit", saveSettings);
      $("#redemption-settings-save").addEventListener("click", saveRedemptionSettings);
    }

    return { bind, load: loadSettings, loadRedemption: loadRedemptionSettings };
  };
})();
