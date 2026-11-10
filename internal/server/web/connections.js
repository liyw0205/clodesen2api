(() => {
  "use strict";
  window.createConnections = ({ api, showToast }) => {
    const $ = selector => document.querySelector(selector);
    const dialog = $("#connection-dialog");
    const model = $("#connection-model");
    const format = $("#connection-format");
    const endpoint = $("#connection-endpoint");
    const output = $("#connection-output");
    const modes = window.modelTypes.modes;
    endpoint.replaceChildren(...modes.map(mode => new Option(mode.label, mode.id)));
    let models = [];
    let secret = "", generation = 0;
    function selectModel() {
      const item = models.find(item => item.id === model.value);
      for (const option of endpoint.options) option.disabled = Boolean(item?.known && !item.endpoints.includes(modes.find(mode => mode.id === option.value).path));
      endpoint.value = modes.find(mode => item?.endpoints.includes(mode.path))?.id || "chat";
      render();
    }
    function render() {
      const base = location.origin + "/v1";
      const key = secret || "YOUR_API_KEY";
      const id = model.value || "MODEL_ID";
      const mode = modes.find(mode => mode.id === endpoint.value) || modes[0];
      const prompt = mode.id === "edit" ? "将图片中的背景改为蓝色" : "一座明亮的现代建筑，晴天，写实风格";
      const payload = mode.id === "chat" ? { model: id, messages: [{ role: "user", content: "你好" }] } : mode.id === "responses" ? { model: id, input: "你好" } : { model: id, prompt };
      if (format.value === "python") {
        const header = "from openai import OpenAI\n\nclient = OpenAI(base_url=" + JSON.stringify(base) + ", api_key=" + JSON.stringify(key) + ")\n";
        const modelArg = "    model=" + JSON.stringify(id) + ",\n";
        if (mode.id === "chat") output.value = header + "response = client.chat.completions.create(\n" + modelArg + "    messages=[{\"role\": \"user\", \"content\": \"你好\"}],\n)\nprint(response.choices[0].message.content)";
        else if (mode.id === "responses") output.value = header + "response = client.responses.create(\n" + modelArg + "    input=\"你好\",\n)\nprint(response.output_text)";
        else if (mode.id === "image") output.value = header + "response = client.images.generate(\n" + modelArg + "    prompt=" + JSON.stringify(prompt) + ",\n)\nprint(response.model_dump_json())";
        else output.value = header + "with open(\"input.png\", \"rb\") as image:\n    response = client.images.edit(\n    " + modelArg + "        prompt=" + JSON.stringify(prompt) + ",\n        image=image,\n    )\nprint(response.model_dump_json())";
      }
      else if (format.value === "client") output.value = JSON.stringify({ base_url: base, api_key: key, model: id, endpoint: mode.path }, null, 2);
      else {
        const quote = value => "'" + value.replaceAll("'", "'\\''") + "'";
        const lines = ["curl " + quote(location.origin + mode.path), "  -H " + quote("Authorization: Bearer " + key)];
        if (mode.id === "edit") lines.push("  -F " + quote("model=" + id), "  -F " + quote("prompt=" + prompt), "  -F 'image[]=@input.png'");
        else lines.push("  -H 'Content-Type: application/json'", "  -d " + quote(JSON.stringify(payload, null, 2)));
        output.value = lines.join(" \\\n");
      }
    }
    async function open(oneTimeSecret = "") {
      const epoch = ++generation;
      secret = oneTimeSecret;
      models = [];
      model.replaceChildren(new Option("MODEL_ID", "MODEL_ID"));
      $("#connection-error").textContent = "";
      selectModel();
      dialog.showModal();
      const [catalog, policies] = await Promise.allSettled([api("/admin/api/models"), api("/admin/api/model-policies")]);
      if (epoch !== generation || !dialog.open) return;
      // A missing policy snapshot cannot safely determine which public names are enabled.
      if (policies.status === "fulfilled") {
        models = window.modelTypes.choices(catalog.status === "fulfilled" ? catalog.value.data || [] : [], policies.value.models || []);
        if (models.length) model.replaceChildren(...models.map(item => new Option(item.id + (item.types.length ? " · " + item.types.join(" / ") : ""), item.id)));
        selectModel();
      }
      const errors = [catalog, policies].filter(result => result.status === "rejected").map(result => result.reason.message);
      $("#connection-error").textContent = errors.join("；");
    }
    function clear() { generation++; secret = ""; models = []; output.value = ""; dialog.close(); }
    dialog.addEventListener("close", () => { if (!dialog.open) clear(); });
    dialog.addEventListener("cancel", event => { event.preventDefault(); clear(); });
    dialog.querySelectorAll("[data-close]").forEach(button => button.addEventListener("click", clear));
    model.addEventListener("change", selectModel);
    endpoint.addEventListener("change", render);
    format.addEventListener("change", render);
    $("#connection-copy").addEventListener("click", async () => {
      const epoch = generation;
      const value = output.value;
      const isCurrent = () => epoch === generation && dialog.open && output.value === value;
      $("#connection-error").textContent = "";
      try { await window.copyText(value, { target: output, isCurrent }); if (isCurrent()) showToast("接入配置已复制"); }
      catch (_) { if (isCurrent()) { output.focus(); output.select(); $("#connection-error").textContent = "自动复制失败，请复制已选中的配置。"; } }
    });
    $("#connections-open").addEventListener("click", () => { void open(); });
    $("#key-connect").addEventListener("click", () => {
      const value = $("#key-secret").value;
      if (!value) return;
      $("#key-secret-dialog").close();
      void open(value);
    });
    return { clear };
  };
})();
