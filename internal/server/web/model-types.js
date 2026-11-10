(() => {
  "use strict";
  const modes = [
    { id: "chat", path: "/v1/chat/completions", label: "对话 · Chat", type: "对话" },
    { id: "responses", path: "/v1/responses", label: "对话 · Responses", type: "对话" },
    { id: "image", path: "/v1/images/generations", label: "文生图", type: "文生图" },
    { id: "edit", path: "/v1/images/edits", label: "图生图", type: "图生图" },
  ];
  const aliases = new Map([
    ["chat", "chat"], ["chat-completions", "chat"], ["conversation", "chat"], ["对话", "chat"], ["text", "chat"], ["text-generation", "chat"], ["llm", "chat"],
    ["responses", "responses"], ["response", "responses"],
    ["image", "image"], ["images", "image"], ["image-generation", "image"], ["text-to-image", "image"], ["text2image", "image"], ["txt2img", "image"], ["t2i", "image"], ["文生图", "image"],
    ["image-edit", "edit"], ["image-editing", "edit"], ["image-to-image", "edit"], ["image2image", "edit"], ["img2img", "edit"], ["i2i", "edit"], ["图生图", "edit"],
  ]);
  function endpoint(value) {
    const name = value.trim().toLowerCase().replaceAll("_", "-");
    const path = name.startsWith("/v1/") ? name : "/v1/" + name.replace(/^\//, "");
    return modes.find(mode => mode.path === path || mode.id === aliases.get(name))?.path;
  }
  function metadata(model) {
    const found = new Set();
    function visit(value, depth = 0) {
      if (depth > 4) return;
      if (typeof value === "string") { const path = endpoint(value); if (path) found.add(path); }
      else if (Array.isArray(value)) value.forEach(item => visit(item, depth + 1));
      else if (value && typeof value === "object" && value.enabled !== false && value.supported !== false) {
        for (const [key, item] of Object.entries(value)) {
          if (item === false || item === null || item?.enabled === false || item?.supported === false) continue;
          const path = endpoint(key);
          if (path) found.add(path);
          visit(item, depth + 1);
        }
      }
    }
    for (const key of ["endpoints", "supported_endpoints", "supportedEndpoints"]) visit(model?.[key]);
    if (found.size) return modes.filter(mode => found.has(mode.path)).map(mode => mode.path);
    for (const key of ["capabilities", "type", "model_type", "modelType", "task", "tasks"]) visit(model?.[key]);
    const outputs = model?.output_modalities || model?.architecture?.output_modalities || [];
    const inputs = model?.input_modalities || model?.architecture?.input_modalities || [];
    if (Array.isArray(outputs) && outputs.includes("image")) {
      found.add(modes[2].path);
      if (Array.isArray(inputs) && inputs.includes("image")) found.add(modes[3].path);
    }
    return modes.filter(mode => found.has(mode.path)).map(mode => mode.path);
  }
  function mapped(id) {
    const name = String(id || "").toLowerCase().split("/").pop();
    if (/^(gpt-image|nano-banana|grok-imagine-image)/.test(name) || /^gemini-.*(?:image|banana)/.test(name)) return [modes[2].path, modes[3].path];
    if (/^dall-e-2(?:$|-)/.test(name)) return [modes[2].path, modes[3].path];
    if (/^(?:flux).*(?:kontext|fill)/.test(name)) return [modes[3].path];
    if (/^(?:dall-e-3|midjourney|flux|stable-diffusion|sdxl)/.test(name)) return [modes[2].path];
    if (/^(?:gpt-|chatgpt-|o[134](?:$|-)|claude|cldude|deepseek|qwen|gemini|grok-|llama|mimo-|minimax-)/.test(name)) return [modes[0].path, modes[1].path];
    return [];
  }
  function resolve(model, models = [], rule = () => null) {
    let id = model.id, enabled = true, configured = null;
    const seen = new Set();
    while (!seen.has(id)) {
      seen.add(id);
      const item = rule(id);
      enabled = enabled && item?.enabled !== false;
      if (item?.endpoints?.length) configured = configured === null ? [...item.endpoints] : configured.filter(path => item.endpoints.includes(path));
      if (!item || !item.upstreamId || item.upstreamId === id) break;
      id = item.upstreamId;
      if (seen.has(id)) enabled = false;
    }
    const effective = models.find(item => item.id === id) || (id === model.id ? model : { id });
    const upstream = metadata(effective);
    const endpoints = configured !== null ? modes.filter(mode => configured.includes(mode.path)).map(mode => mode.path) : upstream.length ? upstream : mapped(id);
    const source = configured !== null ? "接口能力配置" : upstream.length ? "上游信息" : endpoints.length ? "本地映射" : "类型未知";
    const types = [...new Set(modes.filter(mode => endpoints.includes(mode.path)).map(mode => mode.type))];
    return { endpoints, types, source, enabled: enabled && (configured === null || endpoints.length > 0), known: configured !== null || endpoints.length > 0 };
  }
  function choices(models, policies) {
    const rule = id => policies.find(item => item.sourceId === id);
    const catalog = [...models];
    for (const item of policies) if (!catalog.some(model => model.id === item.sourceId)) catalog.push({ id: item.sourceId });
    return catalog.map(model => ({ id: rule(model.id)?.publicId || model.id, ...resolve(model, catalog, rule) })).filter(item => item.id && item.enabled);
  }
  window.modelTypes = { modes, resolve, choices };
})();
