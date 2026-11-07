(() => {
  "use strict";
  window.createPlayground = ({ element }) => {
    const open = element("button", "button button-secondary", "调用测试台"); open.id = "playground-open"; open.type = "button";
    document.querySelector("#analytics-view .page-heading").append(open);
    const dialog = element("dialog", "dialog-shell"), form = element("form", "dialog-content"), heading = element("div", "dialog-heading"); dialog.id = "playground-dialog"; dialog.setAttribute("aria-labelledby", "playground-title");
    const title = element("h2", "", "调用测试台"); title.id = "playground-title"; const close = element("button", "icon-button", "×"); close.type = "button"; close.setAttribute("aria-label", "关闭"); heading.append(title, close);
    const key = element("input"), model = element("input"), mode = element("select"), stream = element("input"), prompt = element("textarea"), consume = element("input");
    key.id = "playground-key"; key.type = "password"; key.autocomplete = "off"; key.required = true; key.spellcheck = false;
    model.id = "playground-model"; model.required = true; model.autocomplete = "off"; model.maxLength = 256;
    mode.id = "playground-mode"; mode.append(new Option("文本", "text"), new Option("图片", "image")); stream.id = "playground-stream"; stream.type = "checkbox";
    prompt.id = "playground-prompt"; prompt.rows = 4; prompt.required = true; prompt.maxLength = 16000; prompt.autocomplete = "off";
    consume.id = "playground-consume"; consume.type = "checkbox"; consume.required = true;
    const field = (label, input) => { const node = element("label", input.type === "checkbox" ? "checkbox-setting" : "", input.type === "checkbox" ? undefined : label); if (input.type === "checkbox") node.append(input, element("span", "", label)); else node.append(input); return node; };
    const grid = element("div", "settings-grid"); grid.append(field("模型 ID", model), field("类型", mode)); const streamField = field("流式输出", stream);
    const send = element("button", "button button-primary", "发送请求"), cancel = element("button", "button button-secondary", "取消请求"), status = element("p", "field-note"), error = element("p", "form-error"), output = element("pre", "playground-output"), images = element("div", "playground-images"), actions = element("div", "dialog-actions");
    send.id = "playground-send"; send.type = "submit"; cancel.id = "playground-cancel"; cancel.type = "button"; cancel.disabled = true; status.id = "playground-status"; status.setAttribute("role", "status"); error.id = "playground-error"; error.setAttribute("role", "alert"); output.id = "playground-output"; output.setAttribute("aria-live", "polite"); images.id = "playground-images";
    actions.append(cancel, send); form.append(heading, field("API 密钥", key), grid, streamField, field("输入", prompt), field("本次请求可能消耗积分，取消后也可能已产生用量", consume), actions, status, error, output, images); dialog.append(form); document.body.append(dialog);
    let controller = null, generation = 0;
    function busy(on) { send.disabled = on; cancel.disabled = !on; for (const input of [key, model, mode, stream, prompt, consume]) input.disabled = on; }
    function clear() { generation++; controller?.abort(); controller = null; form.reset(); key.value = prompt.value = output.textContent = status.textContent = error.textContent = ""; images.replaceChildren(); busy(false); streamField.hidden = false; dialog.close(); }
    function append(text) { if (output.textContent.length + text.length > 1000000) throw new Error("输出超过显示上限"); output.textContent += text; }
    async function readStream(response, epoch) {
      const reader = response.body.getReader(), decoder = new TextDecoder(); let buffer = "", done = false;
      function frame(value) {
        const data = value.split("\n").filter(line => line.startsWith("data:")).map(line => line.slice(5).trimStart()).join("\n"); if (!data) return;
        if (data.trim() === "[DONE]") { done = true; return; }
        const event = JSON.parse(data); if (event.error) throw new Error(event.error.message || "流式响应失败");
        append(event.choices?.[0]?.delta?.content || event.choices?.[0]?.text || "");
      }
      try {
        while (!done) { const chunk = await reader.read(); if (epoch !== generation) return; buffer += decoder.decode(chunk.value || new Uint8Array(), { stream: !chunk.done }); buffer = buffer.replaceAll("\r\n", "\n"); if (buffer.length > 1000000) throw new Error("流式片段过大"); let at; while ((at = buffer.indexOf("\n\n")) !== -1) { frame(buffer.slice(0, at)); buffer = buffer.slice(at + 2); if (done) break; } if (chunk.done) { if (buffer.trim() && !done) frame(buffer); if (!done) throw new Error("流式连接提前结束，保留已收到内容"); break; } }
      } finally { await reader.cancel().catch(() => {}); reader.releaseLock(); }
    }
    form.addEventListener("submit", async event => {
      event.preventDefault(); if (controller || !consume.checked) return;
      const epoch = ++generation; controller = new AbortController(); const active = controller; const timer = window.setTimeout(() => active.abort(new DOMException("请求超时", "TimeoutError")), 180000);
      output.textContent = error.textContent = ""; images.replaceChildren(); status.textContent = "请求处理中"; busy(true);
      const image = mode.value === "image", streaming = !image && stream.checked;
      const body = image ? { model: model.value.trim(), prompt: prompt.value, n: 1, size: "1024x1024", response_format: "b64_json" } : { model: model.value.trim(), messages: [{ role: "user", content: prompt.value }], stream: streaming };
      try {
        const response = await fetch(image ? "/v1/images/generations" : "/v1/chat/completions", { method: "POST", credentials: "omit", cache: "no-store", headers: { "Content-Type": "application/json", Authorization: "Bearer " + key.value.trim() }, body: JSON.stringify(body), signal: active.signal });
        if (epoch !== generation) return;
        const requestID = response.headers.get("X-Request-ID"); status.textContent = "HTTP " + response.status + (requestID ? " · " + requestID : "");
        if (!response.ok) { const value = await response.json().catch(() => null); if (response.status === 401) key.value = ""; throw new Error(value?.error?.message || value?.error || "请求失败（" + response.status + "）"); }
        if (streaming && response.headers.get("Content-Type")?.includes("text/event-stream")) await readStream(response, epoch);
        else {
          const value = await response.json(); if (epoch !== generation) return;
          if (image) {
            for (const item of (value.data || []).slice(0, 4)) {
              let src = "";
              if (typeof item.b64_json === "string" && item.b64_json.length < 16000000 && /^[A-Za-z0-9+/=\s]+$/.test(item.b64_json)) src = "data:image/png;base64," + item.b64_json;
              else if (item.url) { const url = new URL(item.url, location.origin); if (url.protocol === "https:" || url.origin === location.origin) src = url.href; }
              if (src) { const img = element("img"); img.alt = "生成结果"; img.referrerPolicy = "no-referrer"; img.addEventListener("error", () => { if (epoch === generation) error.textContent = "图片加载失败"; }); img.src = src; images.append(img); }
            }
            if (!images.children.length) throw new Error("响应中没有可显示的图片");
          } else append(value.choices?.[0]?.message?.content || JSON.stringify(value, null, 2));
        }
        if (epoch === generation) status.textContent += " · 已完成";
      } catch (failure) { if (epoch === generation) { error.textContent = active.signal.aborted ? active.signal.reason?.name === "TimeoutError" ? "请求超时，结果可能已产生用量" : "请求已取消，结果可能已产生用量" : failure.message; status.textContent = "请求已结束"; } }
      finally { window.clearTimeout(timer); if (epoch === generation) { controller = null; consume.checked = false; busy(false); } }
    });
    open.addEventListener("click", () => { dialog.showModal(); key.focus(); }); close.addEventListener("click", clear); dialog.addEventListener("cancel", event => { event.preventDefault(); clear(); }); dialog.addEventListener("close", () => { if (!dialog.open) clear(); });
    cancel.addEventListener("click", () => controller?.abort()); mode.addEventListener("change", () => { streamField.hidden = mode.value === "image"; });
    return { clear };
  };
})();
