(() => {
  "use strict";
  window.createPlayground = ({ element, getModels, onUnauthorized, onComplete }) => {
    const modes = [
      { id: "chat", label: "Chat", path: "/v1/chat/completions" },
      { id: "responses", label: "Responses", path: "/v1/responses" },
      { id: "image", label: "文生图", path: "/v1/images/generations" },
      { id: "edit", label: "图生图", path: "/v1/images/edits" },
    ];
    const maxOutput = 1000000, maxFileSize = 20 * 1024 * 1024;
    const open = element("button", "button button-secondary", "调用测试台");
    open.id = "playground-open";
    open.type = "button";
    document.querySelector("#analytics-view .page-heading").append(open);
    const dialog = element("dialog", "dialog-shell"), form = element("form", "dialog-content");
    dialog.id = "playground-dialog";
    dialog.setAttribute("aria-labelledby", "playground-title");
    const heading = element("div", "dialog-heading"), title = element("h2", "", "调用测试台");
    title.id = "playground-title";
    function closeButton(label) {
      const button = element("button", "icon-button");
      button.type = "button";
      button.title = label;
      button.setAttribute("aria-label", label);
      const icon = document.createElementNS("http://www.w3.org/2000/svg", "svg");
      const use = document.createElementNS(icon.namespaceURI, "use");
      icon.setAttribute("aria-hidden", "true");
      icon.setAttribute("viewBox", "0 0 24 24");
      icon.setAttribute("fill", "none");
      icon.setAttribute("stroke", "currentColor");
      icon.setAttribute("stroke-width", "2");
      icon.setAttribute("stroke-linecap", "round");
      use.setAttribute("href", "/admin/navigation-icons.svg#x");
      icon.append(use);
      button.append(icon);
      return button;
    }
    const close = closeButton("关闭");
    heading.append(title, close);
    function field(label, input) {
      const node = element("label", input.type === "checkbox" ? "checkbox-setting" : "playground-field");
      if (input.type === "checkbox") node.append(input, element("span", "", label));
      else node.append(element("span", "", label), input);
      return node;
    }
    const model = element("input"), stream = element("input");
    const prompt = element("textarea"), consume = element("input"), size = element("select");
    model.id = "playground-model";
    model.required = true;
    model.autocomplete = "off";
    model.spellcheck = false;
    model.maxLength = 256;
    model.setAttribute("list", "playground-models");
    const modelList = element("datalist");
    modelList.id = "playground-models";
    stream.id = "playground-stream";
    stream.type = "checkbox";
    stream.checked = stream.defaultChecked = true;
    prompt.id = "playground-prompt";
    prompt.rows = 5;
    prompt.required = true;
    prompt.maxLength = 16000;
    prompt.autocomplete = "off";
    consume.id = "playground-consume";
    consume.type = "checkbox";
    consume.required = true;
    size.id = "playground-size";
    for (const value of ["1024x1024", "1536x1024", "1024x1536", "auto"]) size.append(new Option(value === "auto" ? "自动" : value, value));
    const type = element("fieldset", "playground-modes");
    type.id = "playground-mode";
    type.append(element("legend", "sr-only", "调用类型"));
    const modeInputs = modes.map(item => {
      const input = element("input");
      input.type = "radio";
      input.name = "playground-mode";
      input.value = item.id;
      input.id = "playground-mode-" + item.id;
      input.checked = input.defaultChecked = item.id === "chat";
      const label = element("label");
      label.append(input, element("span", "", item.label));
      type.append(label);
      input.addEventListener("change", () => setMode(item.id));
      return input;
    });
    const endpoint = element("code", "playground-endpoint");
    endpoint.id = "playground-endpoint";
    const streamField = field("流式输出", stream), sizeField = field("图片尺寸", size);
    const file = element("input");
    file.id = "playground-file";
    file.type = "file";
    file.className = "sr-only";
    file.tabIndex = -1;
    file.accept = "image/png,image/jpeg,image/webp";
    file.multiple = true;
    file.setAttribute("aria-label", "原图");
    file.setAttribute("aria-describedby", "playground-file-note");
    const upload = element("div", "playground-upload"), previews = element("div", "playground-previews");
    const picker = element("div", "playground-file-picker"), chooseFiles = element("button", "button button-secondary", "选择原图");
    const fileCount = element("span", "field-note", "未选择原图");
    chooseFiles.type = "button";
    chooseFiles.addEventListener("click", () => file.click());
    picker.append(chooseFiles, fileCount, file);
    const fileNote = element("p", "field-note", "PNG / JPEG / WebP，最多 10 张，每张不超过 20 MB");
    fileNote.id = "playground-file-note";
    previews.id = "playground-previews";
    upload.append(element("span", "", "原图"), picker, fileNote, previews);
    const controls = element("div", "playground-controls"), body = element("div", "playground-body");
    controls.append(field("模型 ID", model), modelList, streamField, sizeField, upload, field("输入", prompt));
    const result = element("section", "playground-result"), resultTitle = element("h3", "", "调用结果");
    const status = element("p", "field-note"), error = element("p", "form-error");
    const output = element("pre", "playground-output"), images = element("div", "playground-images");
    const usage = element("p", "field-note"), details = element("details", "playground-details"), raw = element("pre", "playground-output");
    status.id = "playground-status";
    status.setAttribute("role", "status");
    error.id = "playground-error";
    error.setAttribute("role", "alert");
    output.id = "playground-output";
    output.setAttribute("aria-live", "polite");
    images.id = "playground-images";
    usage.id = "playground-usage";
    raw.id = "playground-details";
    details.append(element("summary", "", "响应详情"), raw);
    result.append(resultTitle, status, error, usage, output, images, details);
    body.append(controls, result);
    const footer = element("div", "playground-footer"), actions = element("div", "dialog-actions");
    const send = element("button", "button button-primary", "发送请求"), cancel = element("button", "button button-secondary", "取消请求");
    send.id = "playground-send";
    send.type = "submit";
    cancel.id = "playground-cancel";
    cancel.type = "button";
    cancel.disabled = true;
    actions.append(cancel, send);
    footer.append(field("本次请求可能消耗积分，取消后也可能已产生用量", consume), actions);
    form.append(heading, type, endpoint, body, footer);
    dialog.append(form);
    document.body.append(dialog);

    let controller = null, generation = 0, uploadGeneration = 0, uploadPending = false;
    let selectedMode = "chat", selectedFiles = [], previewURLs = [];
    function imageMode() { return selectedMode === "image" || selectedMode === "edit"; }
    function busy() {
      const on = !!controller;
      for (const input of [model, stream, prompt, consume, size, file, chooseFiles, ...modeInputs, ...previews.querySelectorAll("button")]) input.disabled = on;
      send.disabled = on || uploadPending;
      send.textContent = on ? "请求处理中" : uploadPending ? "读取原图中" : "发送请求";
      cancel.disabled = !on;
      file.disabled = on || selectedMode !== "edit";
      fileCount.textContent = uploadPending ? "正在读取原图" : selectedFiles.length ? "已选择 " + selectedFiles.length + " 张" : "未选择原图";
      size.disabled = on || !imageMode();
      stream.disabled = on || imageMode();
      form.setAttribute("aria-busy", String(on || uploadPending));
    }
    function resetResult() {
      output.textContent = raw.textContent = usage.textContent = status.textContent = error.textContent = "";
      images.replaceChildren();
      details.hidden = true;
      details.open = false;
    }
    function revealResult() {
      if (window.matchMedia("(max-width: 680px)").matches) {
        dialog.scrollTop += result.getBoundingClientRect().top - dialog.getBoundingClientRect().top - 16;
      }
    }
    function clearFiles() {
      uploadGeneration++;
      uploadPending = false;
      for (const url of previewURLs) URL.revokeObjectURL(url);
      previewURLs = [];
      selectedFiles = [];
      previews.replaceChildren();
      file.value = "";
    }
    function setMode(value) {
      if (controller) return;
      if (selectedMode !== value) {
        clearFiles();
        resetResult();
        consume.checked = false;
      }
      selectedMode = value;
      for (const input of modeInputs) input.checked = input.value === value;
      endpoint.textContent = "POST " + modes.find(item => item.id === value).path;
      streamField.hidden = imageMode();
      sizeField.hidden = !imageMode();
      upload.hidden = value !== "edit";
      busy();
    }
    function clear() {
      generation++;
      controller?.abort();
      controller = null;
      clearFiles();
      form.reset();
      resetResult();
      prompt.value = model.value = "";
      modelList.replaceChildren();
      setMode("chat");
      if (dialog.open) dialog.close();
    }
    function renderPreviews() {
      for (const url of previewURLs) URL.revokeObjectURL(url);
      previewURLs = [];
      previews.replaceChildren();
      selectedFiles.forEach((item, index) => {
        const figure = element("figure"), img = element("img"), caption = element("figcaption");
        const url = URL.createObjectURL(item), remove = closeButton("移除 " + item.name);
        previewURLs.push(url);
        img.src = url;
        img.alt = item.name;
        caption.append(element("span", "", item.name), remove);
        remove.addEventListener("click", () => {
          selectedFiles.splice(index, 1);
          file.value = "";
          renderPreviews();
          consume.checked = false;
          busy();
        });
        figure.append(img, caption);
        previews.append(figure);
      });
    }
    file.addEventListener("change", async () => {
      const files = [...file.files];
      clearFiles();
      const epoch = uploadGeneration;
      error.textContent = "";
      consume.checked = false;
      uploadPending = true;
      busy();
      try {
        if (files.length > 10) throw new Error("最多上传 10 张原图");
        for (const item of files) {
          if (!item.size || item.size > maxFileSize) throw new Error("原图不能为空，且每张不能超过 20 MB");
          const bytes = new Uint8Array(await item.slice(0, 12).arrayBuffer());
          const png = [137, 80, 78, 71, 13, 10, 26, 10].every((value, index) => bytes[index] === value);
          const jpeg = bytes[0] === 255 && bytes[1] === 216 && bytes[2] === 255;
          const webp = String.fromCharCode(...bytes.slice(0, 4)) === "RIFF" && String.fromCharCode(...bytes.slice(8, 12)) === "WEBP";
          if (!png && !jpeg && !webp) throw new Error("原图仅支持 PNG、JPEG 或 WebP");
        }
        if (epoch !== uploadGeneration) return;
        selectedFiles = files;
        renderPreviews();
      } catch (failure) {
        if (epoch !== uploadGeneration) return;
        error.textContent = failure.message;
        revealResult();
      } finally {
        if (epoch === uploadGeneration) { uploadPending = false; busy(); }
      }
    });
    function append(text) {
      if (typeof text !== "string") return;
      if (output.textContent.length + text.length > maxOutput) throw new Error("输出超过显示上限");
      const first = !output.textContent && !!text;
      output.textContent += text;
      if (first) revealResult();
    }
    function responseText(value) {
      if (selectedMode === "chat") {
        const content = value.choices?.[0]?.message?.content;
        return typeof content === "string" ? content : value.choices?.[0]?.message?.refusal || "";
      }
      return (value.output || []).flatMap(item => item.content || []).map(item => item.text || item.refusal || "").join("") || value.output_text || "";
    }
    function showMetadata(value) {
      const parts = [], counts = value.usage;
      if (counts) for (const [name, label] of [["input_tokens", "输入"], ["prompt_tokens", "输入"], ["output_tokens", "输出"], ["completion_tokens", "输出"], ["total_tokens", "总 Token"], ["credits", "积分"]]) {
        if (typeof counts[name] === "number") parts.push(label + " " + counts[name]);
      }
      usage.textContent = parts.join(" · ");
      const json = JSON.stringify(value, (name, item) => name === "b64_json" && typeof item === "string" ? "[图片 base64，" + item.length + " 字符]" : item, 2);
      raw.textContent = json.length > maxOutput ? json.slice(0, maxOutput) + "\n[详情已截断]" : json;
      details.hidden = false;
    }
    function responseFailure(value) {
      if (value.error) return typeof value.error === "string" ? value.error : value.error.message || value.error.code || "请求失败";
      if (value.status === "failed") return "Responses 请求失败";
      if (value.status === "incomplete") return "响应未完成：" + (value.incomplete_details?.reason || "上游提前结束");
      return "";
    }
    async function readStream(response, epoch) {
      if (!response.body) throw new Error("响应中没有可读取的数据流");
      const reader = response.body.getReader(), decoder = new TextDecoder();
      let buffer = "", done = false;
      function frame(value) {
        const data = value.split("\n").filter(line => line.startsWith("data:")).map(line => line.slice(5).trimStart()).join("\n");
        if (!data) return;
        if (data.trim() === "[DONE]") { if (selectedMode === "chat") done = true; return; }
        const event = JSON.parse(data);
        if (event.type === "error" || event.error) throw new Error(responseFailure(event) || event.message || "流式响应失败");
        if (selectedMode === "chat") {
          append(event.choices?.[0]?.delta?.content || event.choices?.[0]?.delta?.refusal || event.choices?.[0]?.text || "");
          if (event.usage) showMetadata(event);
          return;
        }
        if (event.type === "response.output_text.delta" || event.type === "response.refusal.delta") append(event.delta);
        if (["response.completed", "response.incomplete", "response.failed"].includes(event.type)) {
          const value = event.response || {};
          if (!output.textContent) append(responseText(value));
          showMetadata(value);
          const failure = responseFailure(value);
          if (failure || event.type !== "response.completed") throw new Error(failure || "Responses 请求未完成");
          done = true;
        }
      }
      try {
        while (!done) {
          const chunk = await reader.read();
          if (epoch !== generation) return;
          buffer += decoder.decode(chunk.value || new Uint8Array(), { stream: !chunk.done });
          buffer = buffer.replaceAll("\r\n", "\n");
          if (buffer.length > maxOutput) throw new Error("流式片段过大");
          let at;
          while ((at = buffer.indexOf("\n\n")) !== -1) {
            frame(buffer.slice(0, at));
            buffer = buffer.slice(at + 2);
            if (done) break;
          }
          if (chunk.done) {
            if (buffer.trim() && !done) frame(buffer);
            if (!done) throw new Error("流式连接提前结束，保留已收到内容");
            break;
          }
        }
      } finally {
        await reader.cancel().catch(() => {});
        reader.releaseLock();
      }
    }
    function showImages(value, epoch) {
      for (const [index, item] of (value.data || []).slice(0, 4).entries()) {
        let src = "";
        if (typeof item.b64_json === "string" && item.b64_json.length < 32000000 && /^[A-Za-z0-9+/=\s]+$/.test(item.b64_json)) {
          const mime = item.b64_json.startsWith("/9j/") ? "image/jpeg" : item.b64_json.startsWith("UklGR") ? "image/webp" : "image/png";
          src = "data:" + mime + ";base64," + item.b64_json;
        } else if (typeof item.url === "string") {
          const url = new URL(item.url, location.origin);
          if (url.protocol === "https:" || url.origin === location.origin) src = url.href;
        }
        if (!src) continue;
        const figure = element("figure"), img = element("img"), link = element("a", "text-button", "下载图片 " + (index + 1));
        img.alt = "生成结果 " + (index + 1);
        img.referrerPolicy = "no-referrer";
        img.addEventListener("error", () => { if (epoch === generation) error.textContent = "图片加载失败"; });
        img.src = src;
        link.href = src;
        link.download = "result-" + (index + 1) + "." + (src.startsWith("data:image/jpeg") ? "jpg" : src.startsWith("data:image/webp") ? "webp" : "png");
        link.target = "_blank";
        link.rel = "noopener noreferrer";
        figure.append(img, link);
        if (item.revised_prompt) figure.append(element("figcaption", "field-note", item.revised_prompt));
        images.append(figure);
      }
      if (!images.children.length) throw new Error("响应中没有可显示的图片");
    }
    form.addEventListener("submit", async event => {
      event.preventDefault();
      if (controller || uploadPending || !consume.checked) return;
      if (!model.value.trim() || !prompt.value.trim()) { error.textContent = "请填写模型 ID 和输入"; revealResult(); return; }
      if (selectedMode === "edit" && !selectedFiles.length) { error.textContent = "请上传原图"; revealResult(); return; }
      const epoch = ++generation, active = new AbortController();
      controller = active;
      const started = performance.now();
      const timer = window.setTimeout(() => active.abort(new DOMException("请求超时", "TimeoutError")), 180000);
      resetResult();
      status.textContent = "请求处理中";
      busy();
      const streaming = !imageMode() && stream.checked;
      const headers = {};
      let body;
      if (selectedMode === "edit") {
        body = new FormData();
        for (const [name, value] of Object.entries({ model: model.value.trim(), prompt: prompt.value, size: size.value, n: "1", response_format: "b64_json" })) body.append(name, value);
        for (const item of selectedFiles) body.append("image[]", item, item.name);
      } else {
        headers["Content-Type"] = "application/json";
        const payload = imageMode() ? { model: model.value.trim(), prompt: prompt.value, n: 1, size: size.value, response_format: "b64_json" }
          : selectedMode === "responses" ? { model: model.value.trim(), input: prompt.value, stream: streaming }
            : { model: model.value.trim(), messages: [{ role: "user", content: prompt.value }], stream: streaming };
        body = JSON.stringify(payload);
      }
      let responseStatus = "";
      try {
        const path = "/admin/api/playground" + modes.find(item => item.id === selectedMode).path.slice(3);
        const response = await fetch(path, { method: "POST", credentials: "same-origin", cache: "no-store", headers, body, signal: active.signal });
        if (epoch !== generation) return;
        const requestID = response.headers.get("X-Request-ID");
        responseStatus = "HTTP " + response.status + (requestID ? " · " + requestID : "");
        status.textContent = responseStatus;
        if (!response.ok) {
          const value = await response.json().catch(() => null);
          if (epoch !== generation) return;
          if (response.status === 401 && value?.code === "admin_session_expired") { onUnauthorized(); return; }
          throw new Error(value && responseFailure(value) || "请求失败（" + response.status + "）");
        }
        if (streaming && response.headers.get("Content-Type")?.includes("text/event-stream")) await readStream(response, epoch);
        else {
          const value = await response.json();
          if (epoch !== generation) return;
          showMetadata(value);
          if (imageMode()) showImages(value, epoch);
          else append(responseText(value) || JSON.stringify(value, null, 2));
          const failure = responseFailure(value);
          if (failure) throw new Error(failure);
          if (!imageMode() && value.status && value.status !== "completed") throw new Error("响应尚未完成：" + value.status);
        }
        if (epoch === generation) status.textContent = responseStatus + " · 已完成 · " + ((performance.now() - started) / 1000).toFixed(2) + " 秒";
      } catch (failure) {
        if (epoch === generation) {
          error.textContent = active.signal.aborted ? active.signal.reason?.name === "TimeoutError" ? "请求超时，结果可能已产生用量" : "请求已取消，结果可能已产生用量" : failure.message;
          status.textContent = (responseStatus ? responseStatus + " · " : "") + "请求已结束";
        }
      } finally {
        window.clearTimeout(timer);
        if (epoch === generation) { controller = null; consume.checked = false; busy(); revealResult(); onComplete(); }
      }
    });
    function show(id) {
      const models = getModels();
      modelList.replaceChildren(...models.map(item => new Option(item.id, item.id)));
      if (id) {
        model.value = id;
        const item = models.find(item => item.id === id);
        const compatible = modes.find(mode => item?.endpoints?.includes(mode.path));
        setMode(compatible?.id || "chat");
      }
      if (!dialog.open) dialog.showModal();
      (model.value ? prompt : model).focus();
    }
    open.addEventListener("click", () => show());
    close.addEventListener("click", clear);
    dialog.addEventListener("cancel", event => { event.preventDefault(); clear(); });
    dialog.addEventListener("close", () => { if (!dialog.open) clear(); });
    cancel.addEventListener("click", () => controller?.abort());
    resetResult();
    setMode("chat");
    return { clear, open: show };
  };
})();
