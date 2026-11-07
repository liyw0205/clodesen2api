(() => {
  "use strict";
  window.createConnections = ({ api, element, showToast }) => {
    const $ = selector => document.querySelector(selector);
    const dialog = $("#connection-dialog");
    const model = $("#connection-model");
    const format = $("#connection-format");
    const output = $("#connection-output");
    let secret = "", generation = 0;
    function render() {
      const base = location.origin + "/v1";
      const key = secret || "YOUR_API_KEY";
      const id = model.value || "MODEL_ID";
      const body = JSON.stringify({ model: id, messages: [{ role: "user", content: "你好" }] }, null, 2);
      if (format.value === "python") output.value = "from openai import OpenAI\n\nclient = OpenAI(base_url=" + JSON.stringify(base) + ", api_key=" + JSON.stringify(key) + ")\nresponse = client.chat.completions.create(\n    model=" + JSON.stringify(id) + ",\n    messages=[{\"role\": \"user\", \"content\": \"你好\"}],\n)\nprint(response.choices[0].message.content)";
      else if (format.value === "client") output.value = JSON.stringify({ base_url: base, api_key: key, model: id }, null, 2);
      else {
        const quote = value => "'" + value.replaceAll("'", "'\\''") + "'";
        output.value = "curl " + quote(base + "/chat/completions") + " \\\n  -H " + quote("Authorization: Bearer " + key) + " \\\n  -H 'Content-Type: application/json' \\\n  -d " + quote(body);
      }
    }
    async function open(oneTimeSecret = "") {
      const epoch = ++generation;
      secret = oneTimeSecret;
      model.replaceChildren(new Option("MODEL_ID", "MODEL_ID"));
      $("#connection-error").textContent = "";
      render();
      dialog.showModal();
      try {
        const result = await api("/admin/api/model-policies");
        if (epoch !== generation || !dialog.open) return;
        const ids = [...new Set((result.models || []).filter(item => item.enabled).map(item => item.publicId))];
        if (ids.length) model.replaceChildren(...ids.map(id => new Option(id, id)));
        render();
      } catch (error) { if (epoch === generation && dialog.open) $("#connection-error").textContent = error.message; }
    }
    function clear() { generation++; secret = ""; output.value = ""; dialog.close(); }
    dialog.addEventListener("close", () => { if (!dialog.open) clear(); });
    dialog.addEventListener("cancel", event => { event.preventDefault(); clear(); });
    dialog.querySelectorAll("[data-close]").forEach(button => button.addEventListener("click", clear));
    model.addEventListener("change", render);
    format.addEventListener("change", render);
    $("#connection-copy").addEventListener("click", async () => {
      const epoch = generation;
      try { await navigator.clipboard.writeText(output.value); if (epoch === generation) showToast("接入配置已复制"); }
      catch (_) { if (epoch === generation && dialog.open) { output.select(); $("#connection-error").textContent = "自动复制失败，请复制已选中的配置。"; } }
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
