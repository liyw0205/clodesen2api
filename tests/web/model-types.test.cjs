const { test } = require("node:test");
const assert = require("node:assert/strict");
const { readFileSync } = require("node:fs");
const { runInNewContext } = require("node:vm");
const window = {};
runInNewContext(readFileSync("internal/server/web/model-types.js", "utf8"), { window });
const { resolve, choices } = window.modelTypes;
const paths = ids => ids.map(id => "/v1/" + id);
const plain = value => JSON.parse(JSON.stringify(value));

test("model types use upstream capabilities before name mapping and do not mistake vision inputs for image editing", () => {
  for (const [model, expected, source] of [
    [{ id: "gpt-image-2" }, ["images/generations", "images/edits"], "本地映射"],
    [{ id: "nano-banana-2.1" }, ["images/generations", "images/edits"], "本地映射"],
    [{ id: "midjourney-v8.2" }, ["images/generations"], "本地映射"],
    [{ id: "cldude-5.5" }, ["chat/completions", "responses"], "本地映射"],
    [{ id: "gpt-image-2", endpoints: ["/v1/images/edits"], type: "chat" }, ["images/edits"], "上游信息"],
    [{ id: "custom", model_type: "text_to_image" }, ["images/generations"], "上游信息"],
    [{ id: "custom", capabilities: { image_edit: true, chat: false } }, ["images/edits"], "上游信息"],
    [{ id: "custom", capabilities: { image: { enabled: false } } }, [], "类型未知"],
    [{ id: "qwen-vl", input_modalities: ["text", "image"], output_modalities: ["text"] }, ["chat/completions", "responses"], "本地映射"],
    [{ id: "custom", architecture: { input_modalities: ["image", "text"], output_modalities: ["image"] } }, ["images/generations", "images/edits"], "上游信息"],
    [{ id: "unlisted-model" }, [], "类型未知"],
  ]) {
    const actual = resolve(model);
    assert.deepEqual(plain(actual.endpoints), paths(expected), model.id);
    assert.equal(actual.source, source);
  }
});

test("local endpoint policies intersect across remapped models and aliases inherit the target type", () => {
  const models = [{ id: "source", type: "chat" }, { id: "gpt-image-2" }];
  const policies = [
    { sourceId: "source", publicId: "studio", upstreamId: "gpt-image-2", enabled: true, endpoints: [] },
    { sourceId: "gpt-image-2", publicId: "gpt-image-2", upstreamId: "gpt-image-2", enabled: true, endpoints: [] },
  ];
  const rule = id => policies.find(item => item.sourceId === id);
  assert.deepEqual(plain(resolve(models[0], models, rule).types), ["文生图", "图生图"]);
  policies[0].endpoints = paths(["images/edits"]);
  assert.deepEqual(plain(resolve(models[0], models, rule).endpoints), paths(["images/edits"]));
  assert.equal(resolve(models[0], models, rule).source, "接口能力配置");
  policies[1].endpoints = paths(["chat/completions"]);
  assert.equal(resolve(models[0], models, rule).enabled, false);
  policies[1].endpoints = [];
  policies[1].enabled = false;
  assert.deepEqual(plain(choices(models, policies)), []);
  policies[1].enabled = true;
  policies[1].upstreamId = "source";
  assert.equal(resolve(models[0], models, rule).enabled, false);
});
