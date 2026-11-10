const { test, before, after } = require("node:test");
const assert = require("node:assert/strict");
const { spawn } = require("node:child_process");
const { existsSync, mkdirSync } = require("node:fs");
if (process.platform === "android") process.env.PLAYWRIGHT_BROWSERS_PATH ||= "0";
const { chromium } = require("playwright-core");

let fixture, browser, base;
const failures = [];

async function selectView(page, tab) {
  const toggle = page.locator("#sidebar-toggle");
  if (await toggle.isVisible()) await toggle.click();
  await page.locator("#" + tab).click();
}

test("sidebar adapts across screens and supports dismissal, keyboard focus and session cleanup", async () => {
  const context = await browser.newContext({ viewport: { width: 1440, height: 1000 } });
  const page = await context.newPage();
  page.on("pageerror", error => failures.push(error.message));
  await page.route("**/admin/api/update**", route => route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ currentVersion: "test", sources: [] }) }));
  try {
    await page.goto(base + "/admin/");
    await page.locator("#admin-token").fill("test-admin-123");
    await page.locator("#login-submit").click();
    await page.locator("#dashboard").waitFor({ state: "visible" });
    const sidebar = page.locator("#workspace-sidebar"), toggle = page.locator("#sidebar-toggle");
    mkdirSync("test-results", { recursive: true });
    for (const width of [1440, 1024, 901]) {
      await page.setViewportSize({ width, height: 844 });
      assert.equal(await toggle.isVisible(), false);
      assert.equal(await sidebar.isVisible(), true);
      const bounds = await sidebar.boundingBox();
      assert.equal(bounds.x, 0); assert.equal(bounds.width, 248);
      assert.equal(await page.locator(".app-column").evaluate(node => node.getBoundingClientRect().left), 248);
      await page.evaluate(() => window.scrollTo(0, 400));
      assert.ok(Math.abs((await sidebar.boundingBox()).y) <= 1, "desktop navigation stays in view");
      assert.equal(await page.evaluate(() => document.documentElement.scrollWidth > innerWidth), false);
      await page.screenshot({ path: "test-results/sidebar-desktop-" + width + ".png" });
    }
    for (const width of [900, 768, 390, 320]) {
      await page.setViewportSize({ width, height: 844 });
      await page.evaluate(() => window.scrollTo(0, 0));
      assert.equal(await sidebar.isVisible(), false);
      assert.equal(await toggle.isVisible(), true);
      assert.equal(await page.locator(".app-column").evaluate(node => node.getBoundingClientRect().left), 0);
      await toggle.click();
      await page.waitForFunction(() => document.querySelector("#workspace-sidebar").getBoundingClientRect().left === 0);
      assert.equal(await sidebar.getAttribute("aria-modal"), "true");
      assert.equal(await page.locator("#accounts-tab").evaluate(node => node === document.activeElement), true);
      assert.equal(await page.locator(".app-column").evaluate(node => node.inert), true);
      await page.keyboard.press("Tab");
      assert.equal(await page.locator(".sidebar-brand").evaluate(node => node === document.activeElement), true);
      await page.keyboard.press("Shift+Tab");
      assert.equal(await page.locator("#accounts-tab").evaluate(node => node === document.activeElement), true);
      const bounds = await sidebar.boundingBox();
      assert.ok(bounds.width <= width - 48); assert.equal(bounds.height, 844);
      assert.equal(await page.evaluate(() => document.documentElement.scrollWidth > innerWidth), false);
      await page.screenshot({ path: "test-results/sidebar-mobile-open-" + width + ".png" });
      await page.keyboard.press("Escape");
      assert.equal(await sidebar.isVisible(), false);
      assert.equal(await toggle.evaluate(node => node === document.activeElement), true);
      assert.equal(await page.locator(".app-column").evaluate(node => node.inert), false);
      await page.screenshot({ path: "test-results/sidebar-mobile-closed-" + width + ".png" });
      await toggle.click();
      await page.locator("#sidebar-backdrop").click({ position: { x: width - 12, y: 100 } });
      assert.equal(await toggle.getAttribute("aria-expanded"), "false");
      await toggle.click(); await page.locator("#sidebar-close").click();
      assert.equal(await sidebar.isVisible(), false);
      // Test the topbar controls geometrically, including their text at 320px.
      const overflow = await page.locator(".topbar").evaluate(node => {
        const controls = [...node.querySelectorAll("button, #service-state, .topbar-context > div")].filter(item => item.getBoundingClientRect().width);
        return controls.some((item, index) => {
          const rect = item.getBoundingClientRect();
          return rect.left < 0 || rect.right > innerWidth || item.scrollWidth > item.clientWidth || controls.slice(index + 1).some(other => {
            const next = other.getBoundingClientRect();
            return rect.left < next.right && rect.right > next.left && rect.top < next.bottom && rect.bottom > next.top;
          });
        });
      });
      assert.equal(overflow, false, "topbar controls fit at " + width);
    }
    await page.evaluate(() => { document.querySelector("#update-source-disclosure").open = true; });
    await toggle.focus(); await page.keyboard.press("Enter"); await page.keyboard.press("Escape");
    assert.equal(await page.locator("#update-source-disclosure").evaluate(node => node.open), true, "drawer Escape does not dismiss a background disclosure");
    assert.equal(await toggle.evaluate(node => node === document.activeElement), true);
    await page.evaluate(() => { document.querySelector("#update-source-disclosure").open = false; });
    await toggle.click();
    await page.keyboard.press("End");
    assert.equal(await page.locator("#settings-tab").getAttribute("aria-selected"), "true");
    await page.keyboard.press("Home"); await page.keyboard.press("ArrowDown");
    assert.equal(await page.locator("#models-tab").evaluate(node => node === document.activeElement), true);
    await page.keyboard.press("ArrowUp"); await page.keyboard.press("End"); await page.keyboard.press("Enter");
    assert.equal(await sidebar.isVisible(), false);
    assert.equal(await page.locator("#settings-view").isVisible(), true);
    assert.equal(await page.locator("#current-view-label").textContent(), "系统设置");
    await page.reload(); await page.locator("#dashboard").waitFor({ state: "visible" });
    assert.equal(await sidebar.isVisible(), false); assert.equal(await page.locator("#settings-view").isVisible(), true);
    await toggle.click(); await page.goBack();
    assert.equal(await sidebar.isVisible(), false);
    await toggle.click(); await page.setViewportSize({ width: 1024, height: 844 });
    await page.waitForFunction(() => {
      const sidebar = document.querySelector("#workspace-sidebar");
      return !sidebar.hasAttribute("aria-modal") && !sidebar.inert && !document.querySelector(".app-column").inert && sidebar.contains(document.activeElement);
    });
    assert.equal(await sidebar.isVisible(), true); assert.equal(await sidebar.getAttribute("aria-modal"), null);
    assert.equal(await page.locator(".app-column").evaluate(node => node.inert), false);
    await page.setViewportSize({ width: 568, height: 320 });
    assert.equal(await sidebar.isVisible(), false);
    await page.waitForFunction(() => document.querySelector("#sidebar-toggle") === document.activeElement);
    const scroll = await page.evaluate(() => { window.scrollTo(0, 200); return scrollY; });
    await toggle.click();
    await page.locator(".sidebar-footer").scrollIntoViewIfNeeded();
    const footer = await page.locator(".sidebar-footer").boundingBox();
    assert.ok(footer.y >= 0 && footer.y + footer.height <= 321, "short-screen navigation can scroll to its footer");
    await page.screenshot({ path: "test-results/sidebar-short-screen.png" });
    await page.emulateMedia({ reducedMotion: "reduce" });
    await page.keyboard.press("Escape"); await toggle.click();
    const selected = await page.locator('.main-tab[aria-selected="true"]').boundingBox();
    assert.ok(selected.y >= 0 && selected.y + selected.height <= 320, "reopening a short drawer reveals the selected view");
    assert.equal(await page.evaluate(() => scrollY), scroll, "opening the drawer preserves the page scroll position");
    assert.ok(await sidebar.evaluate(node => parseFloat(getComputedStyle(node).transitionDuration) <= 0.01));
    // Session invalidation may arrive while the background is inert.
    await page.evaluate(() => document.querySelector("#logout-button").click());
    await page.locator("#login-panel").waitFor({ state: "visible" });
    assert.equal(await page.locator(".app-column").evaluate(node => node.inert), false);
    assert.equal(await page.locator("body").evaluate(node => getComputedStyle(node).overflow), "visible");
    assert.equal(await page.locator("#admin-token").evaluate(node => node === document.activeElement), true);
    await page.locator("#admin-token").fill("test-admin-123"); await page.locator("#login-submit").click();
    await page.locator("#dashboard").waitFor({ state: "visible" });
    assert.equal(await sidebar.isVisible(), false);
    assert.deepEqual(failures, []);
  } finally { await context.close(); }
});

test("settings conflicts preserve local edits and reload concurrent changes before explicit save", async () => {
  const context = await browser.newContext({ viewport: { width: 1440, height: 1000 } });
  const page = await context.newPage();
  page.on("pageerror", error => failures.push(error.message));
  await page.route("**/admin/api/update**", route => route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ currentVersion: "test", sources: [] }) }));
  let baseline;
  try {
    await page.goto(base + "/admin/?view=settings");
    await page.locator("#admin-token").fill("test-admin-123"); await page.locator("#login-submit").click();
    await page.waitForFunction(() => document.querySelector("#settings-cache-accountsSeconds").value !== "");
    baseline = await (await context.request.get(base + "/admin/api/config")).json();
    const ttl = baseline.cache.accountsSeconds + 30;
    await page.locator("#settings-cache-accountsSeconds").fill(String(ttl));
    const threshold = baseline.auth.creditCooldownThreshold + 25;
    await page.locator("#settings-credit-cooldown-threshold").fill(String(threshold));
    await page.locator("#settings-server-addr").fill("127.0.0.1:7791");
    const foreign = structuredClone(baseline);
    foreign.auth.rpm = 17; foreign.upstream.userAgent = "concurrent-editor";
    assert.equal((await context.request.put(base + "/admin/api/config", { data: foreign })).status(), 200);
    await page.locator("#settings-save").click();
    await page.waitForFunction(() => document.querySelector("#settings-error").textContent.includes("保留") && !document.querySelector("#settings-save").disabled);
    assert.equal(await page.locator("#settings-cache-accountsSeconds").inputValue(), String(ttl));
    assert.equal(await page.locator("#settings-credit-cooldown-threshold").inputValue(), String(threshold));
    assert.equal(await page.locator("#settings-server-addr").inputValue(), "127.0.0.1:7791");
    assert.equal(await page.locator("#settings-account-rpm").inputValue(), "17");
    assert.equal(await page.locator("#settings-user-agent").inputValue(), "concurrent-editor");
    assert.match(await page.locator("#settings-preview").textContent(), /待保存 3 项/);
    assert.equal((await (await context.request.get(base + "/admin/api/config")).json()).cache.accountsSeconds, baseline.cache.accountsSeconds);
    await page.locator("#settings-save").click();
    await page.waitForFunction(() => document.querySelector("#settings-preview").hidden && !document.querySelector("#settings-save").disabled);
    const saved = await (await context.request.get(base + "/admin/api/config")).json();
    assert.equal(saved.cache.accountsSeconds, ttl); assert.equal(saved.auth.rpm, 17); assert.equal(saved.upstream.userAgent, "concurrent-editor");
    assert.equal(saved.auth.creditCooldownThreshold, threshold);
  } finally {
    if (baseline) {
      const current = await (await context.request.get(base + "/admin/api/config")).json();
      assert.equal((await context.request.put(base + "/admin/api/config", { data: { ...baseline, revision: current.revision } })).status(), 200);
    }
    await context.close();
  }
});

test("audit details clear on close and logout and late records cannot refill the session", async () => {
  const context = await browser.newContext({ viewport: { width: 1440, height: 1000 } });
  const page = await context.newPage();
  page.on("pageerror", error => failures.push(error.message));
  const result = { records: [
    { requestId: "image-audit", type: "image", prompt: "private audit prompt", model: "fixture-image", stage: "completed", success: true },
    { requestId: "error-audit", type: "non_stream", model: "fixture-model", stage: "completed", statusCategory: "upstream_error", error: "private upstream error", status: 503 },
  ], summary: { total: 2, successes: 1, failures: 1 }, availableAccounts: [], total: 2, page: 1, totalPages: 1 };
  let delay = false, release, entered;
  const pending = new Promise(resolve => { release = resolve; });
  const started = new Promise(resolve => { entered = resolve; });
  await page.route("**/admin/api/update**", route => route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ currentVersion: "test", sources: [] }) }));
  await page.route("**/admin/api/request-records?**", async route => {
    if (delay) { entered(); await pending; }
    await route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify(result) }).catch(() => {});
  });
  try {
    await page.goto(base + "/admin/?view=records&account=retired-account");
    await page.locator("#admin-token").fill("test-admin-123"); await page.locator("#login-submit").click();
    await page.waitForFunction(() => document.querySelectorAll("#request-record-rows tr").length === 2);
    assert.equal(await page.locator("#request-filter-account").inputValue(), "retired-account");
    await page.locator("#request-record-rows").getByRole("button", { name: "查看提示词", exact: true }).click();
    assert.equal(await page.locator("#request-prompt-text").textContent(), "private audit prompt");
    await page.locator("#prompt-dialog button[aria-label=关闭]").click();
    assert.equal(await page.locator("#request-prompt-text").textContent(), "");
    await page.locator("#request-record-rows").getByRole("button", { name: "查看提示词", exact: true }).click();
    await page.keyboard.press("Escape"); assert.equal(await page.locator("#request-prompt-text").textContent(), "");
    await page.locator("#request-record-rows").getByRole("button", { name: "查看报错", exact: true }).click();
    assert.equal(await page.locator("#request-error-text").textContent(), "private upstream error");
    await page.locator("#request-error-dialog button[aria-label=关闭]").click();
    assert.equal(await page.locator("#request-error-text").textContent(), "");
    delay = true;
    await page.locator("#records-refresh").click(); await started;
    await page.locator("#request-record-rows").getByRole("button", { name: "查看提示词", exact: true }).click();
    await page.evaluate(() => document.querySelector("#logout-button").click());
    await page.locator("#login-panel").waitFor({ state: "visible" }); release();
    assert.equal(await page.locator("#request-prompt-text").textContent(), "");
    assert.equal(await page.locator("#request-error-text").textContent(), "");
    assert.equal(await page.locator("#request-record-total").textContent(), "--");
    assert.equal(await page.locator("#request-record-rows tr").count(), 0);
    delay = false;
    await page.locator("#admin-token").fill("test-admin-123"); await page.locator("#login-submit").click();
    await page.waitForFunction(() => document.querySelectorAll("#request-record-rows tr").length === 2);
    assert.equal(await page.locator("#records-refresh").isEnabled(), true);
  } finally { release(); await context.close(); }
});

test("reports expose upgrade coverage, dimensions, CSV and preserve rows on failure", async () => {
  const context = await browser.newContext({ viewport: { width: 1440, height: 1000 } }); const page = await context.newPage(); let created;
  page.on("pageerror", error => failures.push(error.message));
  try {
    await page.goto(base + "/admin/?view=analytics"); await page.locator("#admin-token").fill("test-admin-123"); await page.locator("#login-submit").click();
    await page.waitForFunction(() => document.querySelector("#model-count").textContent !== "等待加载");
    created = await (await context.request.post(base + "/admin/api/keys", { data: { name: "report-fixture" } })).json(); assert.ok(created.key?.id, JSON.stringify(created));
    for (let i = 0; i < 3; i++) assert.equal((await context.request.post(base + "/v1/chat/completions", { headers: { Authorization: "Bearer " + created.secret }, data: { model: "allowed-model", messages: [{ role: "user", content: "report fixture" }] } })).status(), 200);
    await page.locator("#report-dimension").selectOption("key"); await page.locator("#report-range").selectOption("30d");
    await page.waitForFunction(id => document.querySelector("#report-rows").textContent.includes(id), created.key.id);
    assert.match(await page.locator("#report-coverage").textContent(), /历史不完整/);
    const report = await (await context.request.get(base + "/admin/api/reports?range=30d&dimension=key")).json(); const row = report.rows.find(item => item.name === created.key.id);
    assert.equal(row.requests, 3); assert.equal(row.successRate, 100); assert.ok(row.p95Ms >= row.averageMs); assert.equal(report.retentionDays, 31); assert.equal(report.recordLimit, 0);
    const downloadPromise = page.waitForEvent("download"); await page.locator("#report-export").click(); assert.equal((await downloadPromise).suggestedFilename(), "request-report.csv");
    const csv = await context.request.get(base + "/admin/api/reports?range=30d&dimension=key&format=csv"); assert.match(csv.headers()["content-type"], /text\/csv/); assert.match(await csv.text(), new RegExp(created.key.id));
    await page.route("**/admin/api/reports?**", route => route.fulfill({ status: 503, contentType: "application/json", body: JSON.stringify({ error: "report unavailable" }) })); await page.locator("#report-refresh").click();
    await page.waitForFunction(() => document.querySelector("#report-error").textContent.includes("report unavailable")); assert.match(await page.locator("#report-rows").textContent(), new RegExp(created.key.id));
    await page.unroute("**/admin/api/reports?**"); await page.locator("#report-refresh").click(); await page.waitForFunction(() => document.querySelector("#report-error").textContent === "" && !document.querySelector("#report-refresh").disabled);
    for (const width of [1440, 768, 390, 320]) { await page.setViewportSize({ width, height: 844 }); assert.equal(await page.evaluate(() => document.documentElement.scrollWidth > innerWidth), false); assert.equal(await page.locator("#report-rows tr").first().isVisible(), true); await page.screenshot({ path: "test-results/reports-" + width + ".png", fullPage: true }); }
    await context.request.delete(base + "/admin/api/keys/" + created.key.id + "?version=" + created.key.version); created = null;
    await page.locator("#logout-button").click(); await page.locator("#login-panel").waitFor({ state: "visible" }); assert.equal(await page.locator("#report-rows").textContent(), "");
  } finally { if (created?.key?.id && created?.key?.version) await context.request.delete(base + "/admin/api/keys/" + created.key.id + "?version=" + created.key.version).catch(() => {}); await context.close(); }
});

test("routing and update source panels stay inside the viewport and preserve selection", async () => {
  const context = await browser.newContext({ viewport: { width: 390, height: 844 } });
  const page = await context.newPage();
  page.on("pageerror", error => failures.push(error.message));
  let savedSource = { source: "direct", customPrefix: "" };
  const sources = [{ id: "direct", name: "直连" }, ...Array.from({ length: 10 }, (_, i) => ({ id: "source-" + i, name: "加速源 " + (i + 1) })), { id: "custom", name: "自定义" }];
  await page.route("**/admin/api/update", route => {
    if (route.request().method() === "PUT") savedSource = route.request().postDataJSON();
    return route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ currentVersion: "test", availableSources: sources, sourceID: savedSource.source, ok: true, latencyMs: 12 }) });
  });
  await page.route("**/admin/api/config", async route => {
    if (route.request().method() !== "GET") return route.continue();
    const response = await route.fetch(), settings = await response.json();
    settings.update = savedSource;
    return route.fulfill({ response, json: settings });
  });
  const fits = async selector => {
    await page.waitForFunction(selector => {
      const rect = document.querySelector(selector).getBoundingClientRect();
      return rect.width > 0 && rect.left >= 11 && rect.top >= 11 && rect.right <= innerWidth - 11 && rect.bottom <= innerHeight - 11;
    }, selector);
  };
  try {
    await page.goto(base + "/admin/"); await page.locator("#admin-token").fill("test-admin-123"); await page.locator("#login-submit").click();
    await page.waitForFunction(() => document.querySelector("#account-rows").textContent.includes("fixture@example.test"));
    assert.equal(await page.locator("#routing-strategy").evaluate(node => node.tagName), "SUMMARY");
    assert.deepEqual(await page.locator("#routing-panel input").evaluateAll(nodes => nodes.map(node => node.value)), ["fixed", "round_robin", "random"]);
    for (const strategy of ["round_robin", "random", "fixed"]) {
      await page.locator("#routing-strategy").click();
      await page.locator('#routing-panel input[value="' + strategy + '"]').check();
      await page.waitForFunction(() => document.querySelector("#routing-strategy").getAttribute("aria-disabled") === "false");
      assert.equal((await (await context.request.get(base + "/admin/api/settings")).json()).strategy, strategy);
      await page.reload();
      await page.waitForFunction(strategy => document.querySelector('#routing-panel input[value="' + strategy + '"]')?.checked, strategy);
    }
    for (const strategy of ["weighted", "least_inflight", "latency"]) assert.equal((await context.request.put(base + "/admin/api/settings", { data: { strategy } })).status(), 400);
    assert.equal((await context.request.patch(base + "/admin/api/accounts/fixture", { data: { weight: 37 } })).status(), 400);
    const accounts = await (await context.request.get(base + "/admin/api/accounts")).json();
    assert.equal("weight" in accounts.accounts[0], false);
    await page.locator("#account-cards [data-action=edit]").first().click();
    assert.equal(await page.locator("#account-weight").count(), 0); await page.keyboard.press("Escape");
    await page.route("**/admin/api/settings", route => route.request().method() === "PUT" ? route.fulfill({ status: 500, json: { error: "routing save failed" } }) : route.continue());
    await page.locator("#routing-strategy").click(); await page.locator('#routing-panel input[value="random"]').click();
    await page.waitForFunction(() => document.querySelector("#routing-strategy").getAttribute("aria-disabled") === "false");
    assert.equal(await page.locator('#routing-panel input[value="fixed"]').isChecked(), true);
    await page.unroute("**/admin/api/settings");
    mkdirSync("test-results", { recursive: true });
    for (const viewport of [{ width: 1440, height: 844 }, { width: 768, height: 844 }, { width: 390, height: 844 }, { width: 320, height: 568 }, { width: 568, height: 320 }]) {
      await page.setViewportSize(viewport);
      await page.locator("#routing-disclosure").evaluate(node => { node.style.cssText = "position:fixed;right:12px;bottom:12px"; });
      await page.locator("#routing-strategy").click(); await fits("#routing-panel");
      await page.screenshot({ path: "test-results/routing-panel-" + viewport.width + ".png" });
      await page.keyboard.press("Escape");
      assert.equal(await page.locator("#routing-strategy").evaluate(node => node === document.activeElement), true);
      await page.locator("#routing-strategy").focus(); await page.keyboard.press("ArrowDown"); await fits("#routing-panel");
      assert.equal(await page.locator("#routing-panel input:checked").evaluate(node => node === document.activeElement), true);
      await page.keyboard.press("Escape");
    }
    await page.locator("#routing-disclosure").evaluate(node => { node.removeAttribute("style"); });
    await selectView(page, "settings-tab");
    await page.locator("#settings-group-update").click();
    await page.waitForFunction(() => document.querySelectorAll("#update-source-options input").length === 12);
    assert.equal(await page.locator("#settings-strategy option").count(), 3);
    await page.locator("#update-source-disclosure summary").click(); await fits("#update-source-panel");
    await page.evaluate(() => window.scrollBy(0, 80)); await fits("#update-source-panel");
    await page.keyboard.press("Escape");
    for (const viewport of [{ width: 1440, height: 844 }, { width: 768, height: 844 }, { width: 390, height: 844 }, { width: 320, height: 568 }, { width: 568, height: 320 }]) {
      await page.setViewportSize(viewport);
      await page.locator("#update-source-disclosure").evaluate(node => { node.style.cssText = "position:fixed;right:12px;bottom:12px"; });
      await page.locator("#update-source-disclosure summary").click(); await fits("#update-source-panel");
      assert.equal(await page.locator(".update-source-body").evaluate(node => node.scrollHeight > node.clientHeight), true);
      await page.locator('#update-source-options input[value="custom"]').check();
      await fits("#update-source-panel");
      const inputBounds = await page.locator("#update-custom-prefix").boundingBox();
      await page.screenshot({ path: "test-results/update-source-panel-" + viewport.width + ".png" });
      assert.ok(inputBounds.y >= 12 && inputBounds.y + inputBounds.height <= viewport.height - 12, JSON.stringify({ viewport, inputBounds }));
      await page.locator("#update-custom-prefix").fill("https://mirror.example.test/");
      await page.screenshot({ path: "test-results/update-source-panel-" + viewport.width + ".png" });
      await page.locator("#update-source-save").click();
      await page.waitForFunction(() => !document.querySelector("#update-source-disclosure").open);
      assert.equal(savedSource.source, "custom");
      assert.equal(savedSource.customPrefix, "https://mirror.example.test/");
      await page.locator("#update-source-disclosure summary").click(); await fits("#update-source-panel");
      await page.setViewportSize({ width: viewport.width, height: viewport.height - 80 }); await fits("#update-source-panel");
      await page.locator("#service-state").click();
      assert.equal(await page.locator("#update-source-disclosure").evaluate(node => node.open), false);
    }
    await page.locator("#update-source-disclosure summary").click();
    await page.evaluate(() => document.querySelector("#logout-button").click());
    await page.locator("#login-panel").waitFor({ state: "visible" });
    assert.equal(await page.locator("#update-source-disclosure").evaluate(node => node.open), false);
    assert.equal(await page.locator("#routing-disclosure").evaluate(node => node.open), false);
  } finally {
    await context.request.put(base + "/admin/api/settings", { data: { strategy: "fixed" } }).catch(() => {});
    await context.close();
  }
});

test("default-off notification settings survive reload", async () => {
  const context = await browser.newContext({ viewport: { width: 390, height: 844 } }); const page = await context.newPage();
  page.on("pageerror", error => failures.push(error.message));
  let alertBaseline, testedChannel;
  try {
    await page.goto(base + "/admin/"); await page.locator("#admin-token").fill("test-admin-123"); await page.locator("#login-submit").click();
    await page.waitForFunction(() => document.querySelector("#account-rows").textContent.includes("fixture@example.test"));
    alertBaseline = await (await context.request.get(base + "/admin/api/alerts")).json();
    await selectView(page, "settings-tab"); await page.locator("#settings-group-notifications").click(); await page.waitForFunction(() => document.querySelector("#alerts-status").textContent.includes("已关闭"));
    assert.equal(await page.locator("#alerts-channel option").count(), 13);
    for (const definition of alertBaseline.definitions) {
      await page.locator("#alerts-channel").selectOption(definition.id);
      assert.equal(await page.locator("#alerts-channel-title").textContent(), definition.name + " 配置");
      assert.deepEqual(await page.locator("#alerts-channel-fields [data-field]").evaluateAll(nodes => nodes.map(node => node.dataset.field)), definition.fields.map(field => field.key));
      assert.match(await page.locator("#alerts-enabled").locator("..").textContent(), new RegExp(definition.name));
    }
    await page.locator("#alerts-channel").selectOption("smtp"); assert.equal(await page.locator("#alerts-field-server").isVisible(), true); assert.equal(await page.locator("#alerts-field-password").getAttribute("type"), "password");
    await page.locator("#alerts-channel").selectOption("bark"); assert.equal(await page.locator("#alerts-field-push").isVisible(), true);
    await page.locator("#alerts-channel").selectOption("webhook");
    await page.locator("#alerts-field-url").fill("http://127.0.0.1:1/notify"); await page.locator("#alerts-enabled").check();
    await page.locator("#alerts-channel").selectOption("ntfy"); assert.equal(await page.locator("#alerts-field-topic").isVisible(), true);
    await page.locator("#alerts-field-url").fill("http://127.0.0.1:1"); await page.locator("#alerts-field-topic").fill("browser-test"); await page.locator("#alerts-enabled").check();
    await page.locator("#alerts-threshold").fill("321"); await page.getByRole("button", { name: "保存通知设置" }).click();
    await page.waitForFunction(() => document.querySelector("#alerts-test-result").textContent === "通知设置已保存");
    let savedAlerts = await (await context.request.get(base + "/admin/api/alerts")).json();
    assert.equal(savedAlerts.settings.channels.webhook.enabled, true); assert.equal(savedAlerts.settings.channels.ntfy.enabled, true);
    assert.equal(savedAlerts.configured.webhook.url, true); assert.equal("url" in savedAlerts.settings.channels.webhook.config, false);
    await page.reload(); await page.locator("#settings-group-notifications").click(); await page.waitForFunction(() => document.querySelector("#alerts-threshold").value === "321"); assert.equal(await page.locator("#alerts-enabled").isChecked(), true);
    await page.route("**/admin/api/alerts/test", async route => { testedChannel = route.request().postDataJSON(); await route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ message: "测试渠道可用" }) }); });
    await page.getByRole("button", { name: "发送测试通知" }).click(); await page.waitForFunction(() => document.querySelector("#alerts-test-result").textContent === "测试渠道可用");
    assert.equal(testedChannel.channel, "webhook"); assert.equal(testedChannel.config.url, "");
    await page.locator("[data-clear=url]").check(); await page.locator("#alerts-enabled").uncheck(); await page.getByRole("button", { name: "保存通知设置" }).click();
    await page.waitForFunction(() => document.querySelector("#alerts-test-result").textContent === "通知设置已保存");
    savedAlerts = await (await context.request.get(base + "/admin/api/alerts")).json();
    assert.equal(savedAlerts.configured.webhook.url, false); assert.equal(savedAlerts.settings.channels.webhook.enabled, false); assert.equal(savedAlerts.settings.channels.ntfy.enabled, true);
    for (const width of [1440, 768, 390, 320]) {
      await page.setViewportSize({ width, height: 844 }); await page.evaluate(() => window.scrollTo(0, 0));
      assert.equal(await page.evaluate(() => document.documentElement.scrollWidth > innerWidth), false);
      const overflow = await page.locator("#notifications-panel").evaluate(panel => [...panel.querySelectorAll("input, textarea, select, button")].some(node => {
        const rect = node.getBoundingClientRect(); return rect.left < 0 || rect.right > innerWidth || node.tagName === "BUTTON" && node.scrollWidth > node.clientWidth;
      }));
      assert.equal(overflow, false, "notification controls fit at " + width);
      await page.screenshot({ path: "test-results/notifications-" + width + ".png", fullPage: true });
    }
  } finally {
    if (alertBaseline) {
      const current = await (await context.request.get(base + "/admin/api/alerts")).json();
      const channels = Object.fromEntries(Object.entries(current.settings.channels || {}).map(([id, item]) => [id, { enabled: false, config: item.config || {} }]));
      const clearSecrets = Object.entries(current.configured || {}).flatMap(([id, fields]) => Object.entries(fields).filter(([, configured]) => configured).map(([key]) => id + "." + key));
      await context.request.put(base + "/admin/api/alerts", { data: { revision: current.revision, settings: { lowBalance: alertBaseline.settings.lowBalance, channels }, clearSecrets } }).catch(() => {});
    }
    await context.close();
  }
});

test("playground calls Chat, Responses, generation and multipart edits with consent and responsive layouts", async () => {
  const context = await browser.newContext({ viewport: { width: 390, height: 844 } }); const page = await context.newPage();
  page.on("pageerror", error => failures.push(error.message));
  const png = Buffer.from("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+jY1sAAAAASUVORK5CYII=", "base64");
  const upload = name => ({ name, mimeType: "image/png", buffer: png });
  const choose = value => page.locator("#playground-mode label").filter({ has: page.locator("input[value=" + value + "]") }).click();
  const send = async () => {
    await page.locator("#playground-consume").check();
    await page.locator("#playground-send").click();
    await page.waitForFunction(() => document.querySelector("#playground-status").textContent.includes("已完成"));
    assert.equal(await page.locator("#playground-consume").isChecked(), false);
    assert.equal(await page.locator("#playground-error").textContent(), "");
  };
  try {
    await page.goto(base + "/admin/?view=analytics"); await page.locator("#admin-token").fill("test-admin-123"); await page.locator("#login-submit").click(); await page.waitForFunction(() => document.querySelector("#model-count").textContent !== "等待加载");
    const keysBefore = await (await context.request.get(base + "/admin/api/keys")).json();
    await page.locator(".model-card").filter({ hasText: "allowed-model" }).getByRole("button", { name: "测试", exact: true }).click();
    assert.equal(await page.locator("#playground-model").inputValue(), "allowed-model");
    assert.equal(await page.locator("#playground-key").count(), 0);
    assert.equal(await page.locator("#playground-stream").isChecked(), true);
    await page.locator("#playground-prompt").fill("fixture test");
    await page.locator("#playground-send").click(); assert.equal(await page.locator("#playground-status").textContent(), "");
    const chatRequest = page.waitForRequest(request => request.url().endsWith("/admin/api/playground/chat/completions"));
    await send(); assert.equal(await page.locator("#playground-output").textContent(), "fixture stream");
    const headers = await (await chatRequest).allHeaders();
    assert.equal(headers.authorization, undefined);
    assert.match(headers.cookie, /clodesen_admin=/, "model tests use the authenticated admin session");
    assert.equal((await chatRequest).postDataJSON().stream, true);
    await page.locator("#playground-prompt").fill("fixture:streaming");
    await page.locator("#playground-consume").check(); await page.locator("#playground-send").click();
    await page.waitForFunction(() => document.querySelector("#playground-output").textContent === "fixture ");
    assert.equal(await page.locator("#playground-send").isDisabled(), true, "first chunk renders while upstream is still generating");
    assert.doesNotMatch(await page.locator("#playground-status").textContent(), /已完成/);
    assert.equal(await page.locator("#playground-output").evaluate(node => {
      const bounds = node.getBoundingClientRect(), footer = document.querySelector(".playground-footer").getBoundingClientRect();
      return bounds.top >= 0 && bounds.bottom <= footer.top;
    }), true, "mobile shows the first streaming chunk immediately");
    await page.locator("#playground-cancel").click();
    await page.waitForFunction(() => document.querySelector("#playground-error").textContent.includes("已取消"));
    assert.equal(await page.locator("#playground-output").textContent(), "fixture ");
    await page.locator("#playground-prompt").fill("fixture test");
    await page.locator("#playground-stream").uncheck(); await send();
    assert.equal(await page.locator("#playground-output").textContent(), "fixture reply");
    await choose("responses"); await page.locator("#playground-stream").uncheck();
    assert.match(await page.locator("#playground-endpoint").textContent(), /\/v1\/responses$/);
    await send(); assert.equal(await page.locator("#playground-output").textContent(), "fixture responses reply");
    assert.match(await page.locator("#playground-usage").textContent(), /总 Token 20/);
    await page.locator("#playground-stream").check(); await send();
    assert.equal(await page.locator("#playground-output").textContent(), "fixture responses stream", "Responses completes without a Chat DONE marker or duplicate final output");
    assert.match(await page.locator("#playground-usage").textContent(), /积分 3/);
    assert.equal(await page.locator("#playground-output").evaluate(node => {
      const bounds = node.getBoundingClientRect(), footer = document.querySelector(".playground-footer").getBoundingClientRect();
      return bounds.top >= 0 && bounds.bottom <= footer.top;
    }), true, "mobile completion reveals the returned text above request actions");
    mkdirSync("test-results", { recursive: true });
    for (const width of [1440, 390]) {
      await page.setViewportSize({ width, height: 844 });
      await page.locator("#playground-dialog").evaluate(node => { node.scrollTop = node.scrollHeight; });
      await page.screenshot({ path: "test-results/playground-responses-" + width + ".png" });
    }
    await choose("image"); assert.equal(await page.locator("#playground-stream").isVisible(), false);
    await page.locator("#playground-size").selectOption("1536x1024"); await send();
    await page.waitForFunction(() => document.querySelector("#playground-images img")?.naturalWidth > 0);
    assert.match(await page.locator("#playground-images").textContent(), /fixture generation:1536x1024/);
    assert.match(await page.locator("#playground-images a").getAttribute("download"), /\.png$/);
    await choose("edit");
    await page.locator("#playground-file").setInputFiles({ name: "fake.png", mimeType: "image/png", buffer: Buffer.from("not an image") });
    await page.waitForFunction(() => document.querySelector("#playground-error").textContent.includes("仅支持"));
    assert.equal(await page.locator("#playground-previews img").count(), 0);
    await page.locator("#playground-file").setInputFiles(Array.from({ length: 11 }, (_, index) => upload("image-" + index + ".png")));
    await page.waitForFunction(() => document.querySelector("#playground-error").textContent.includes("最多上传"));
    await page.locator("#playground-file").setInputFiles({ name: "huge.png", mimeType: "image/png", buffer: Buffer.alloc(20 * 1024 * 1024 + 1) });
    await page.waitForFunction(() => document.querySelector("#playground-error").textContent.includes("20 MB"));
    await page.locator("#playground-consume").check(); await page.locator("#playground-send").click();
    assert.equal(await page.locator("#playground-status").textContent(), "", "invalid uploads cannot send");
    await page.locator("#playground-file").setInputFiles([upload("reference-one.png"), upload("reference-with-a-long-filename-01234567890123456789.png")]);
    await page.waitForFunction(() => document.querySelectorAll("#playground-previews img").length === 2 && [...document.querySelectorAll("#playground-previews img")].every(img => img.naturalWidth > 0));
    await page.locator("#playground-size").selectOption("1024x1536"); await send();
    assert.match(await page.locator("#playground-images").textContent(), /fixture edit:2:1024x1536/, "gateway forwards both original images and selected size intact");
    mkdirSync("test-results", { recursive: true });
    for (const { width, height } of [{ width: 1440, height: 844 }, { width: 768, height: 844 }, { width: 390, height: 844 }, { width: 320, height: 844 }, { width: 320, height: 568 }, { width: 568, height: 320 }]) {
      await page.setViewportSize({ width, height });
      await page.locator("#playground-dialog").evaluate(node => { node.scrollTop = 0; });
      const geometry = await page.locator("#playground-dialog").evaluate(dialog => {
        const bounds = dialog.getBoundingClientRect();
        const actions = [...dialog.querySelectorAll(".playground-footer button")].map(button => button.getBoundingClientRect());
        return { overflow: document.documentElement.scrollWidth > innerWidth || dialog.scrollWidth > dialog.clientWidth,
          inView: bounds.top >= 0 && bounds.bottom <= innerHeight && actions.every(rect => rect.top >= bounds.top && rect.bottom <= bounds.bottom),
          fieldsFit: [...dialog.querySelectorAll(".playground-field, .playground-modes span, figcaption")].every(node => node.scrollWidth <= node.clientWidth + 1) };
      });
      assert.deepEqual(geometry, { overflow: false, inView: true, fieldsFit: true }, "layout at " + width);
      await page.screenshot({ path: "test-results/playground-" + width + (height === 844 ? "" : "-" + height) + ".png" });
    }
    await page.locator("#playground-previews button").first().click();
    assert.equal(await page.locator("#playground-previews img").count(), 1); await send();
    assert.match(await page.locator("#playground-images").textContent(), /fixture edit:1:1024x1536/);
    await choose("chat"); assert.equal(await page.locator("#playground-previews img").count(), 0);
    assert.equal(await page.locator("#playground-stream").isVisible(), true);
    assert.equal(await page.locator("#playground-size").isVisible(), false);
    await choose("edit"); assert.equal(await page.locator("#playground-previews img").count(), 0);
    await page.locator("#playground-file").setInputFiles(upload("clear-on-close.png"));
    await page.waitForFunction(() => document.querySelectorAll("#playground-previews img").length === 1);
    await page.locator("#playground-dialog button[aria-label=关闭]").click();
    assert.equal(await page.locator("#playground-model").inputValue(), "");
    assert.equal(await page.locator("#playground-previews img").count(), 0);
    assert.equal(await page.locator("#playground-prompt").inputValue(), "");
    assert.deepEqual((await (await context.request.get(base + "/admin/api/keys")).json()).keys, keysBefore.keys, "testing does not create client keys");
  } finally { await context.close(); }
});

test("playground preserves partial output, distinguishes upstream auth from session expiry and clears on close", async () => {
  const context = await browser.newContext({ viewport: { width: 390, height: 844 } }); const page = await context.newPage();
  page.on("pageerror", error => failures.push(error.message));
  try {
    await page.goto(base + "/admin/?view=analytics"); await page.locator("#admin-token").fill("test-admin-123"); await page.locator("#login-submit").click(); await page.locator("#dashboard").waitFor({ state: "visible" });
    await page.locator("#playground-open").click(); await page.locator("#playground-model").fill("allowed-model");
    await page.locator("#playground-mode label").filter({ hasText: "Responses" }).click();
    for (const stream of [false, true]) {
      await page.locator("#playground-stream").setChecked(stream);
      for (const [prompt, error] of [["fixture:incomplete", "max_output_tokens"], ["fixture:failed", "fixture response failed"], ...(stream ? [["fixture:truncated", "提前结束"]] : [])]) {
        await page.locator("#playground-prompt").fill(prompt);
        await page.locator("#playground-consume").check(); await page.locator("#playground-send").click();
        await page.waitForFunction(error => document.querySelector("#playground-error").textContent.includes(error), error);
        assert.match(await page.locator("#playground-output").textContent(), /fixture responses/);
        assert.doesNotMatch(await page.locator("#playground-status").textContent(), /已完成/);
      }
    }
    await page.locator("#playground-mode label").filter({ hasText: "Chat" }).click(); await page.locator("#playground-stream").uncheck();
    await page.locator("#playground-prompt").fill("fixture test");
    const chatPath = "**/admin/api/playground/chat/completions";
    await page.route(chatPath, route => route.fulfill({ status: 429, contentType: "application/json", body: JSON.stringify({ error: { message: "fixture rate limit" } }) }));
    await page.locator("#playground-consume").check(); await page.locator("#playground-send").click(); await page.waitForFunction(() => document.querySelector("#playground-error").textContent.includes("fixture rate limit"));
    assert.match(await page.locator("#playground-status").textContent(), /HTTP 429/);
    await page.unroute(chatPath);
    await page.route(chatPath, route => route.fulfill({ status: 401, contentType: "application/json", body: JSON.stringify({ error: { message: "fixture unauthorized" } }) }));
    await page.locator("#playground-consume").check(); await page.locator("#playground-send").click();
    await page.waitForFunction(() => document.querySelector("#playground-error").textContent.includes("fixture unauthorized"));
    assert.equal(await page.locator("#dashboard").isVisible(), true, "upstream 401 keeps the admin session");
    await page.unroute(chatPath);
    let release; const pending = new Promise(resolve => { release = resolve; });
    await page.route(chatPath, async route => { await pending; await route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ choices: [{ message: { content: "late reply" } }] }) }).catch(() => {}); });
    await page.locator("#playground-consume").check(); await page.locator("#playground-send").click(); await page.locator("#playground-cancel").click(); release(); await page.waitForFunction(() => document.querySelector("#playground-error").textContent.includes("已取消")); assert.doesNotMatch(await page.locator("#playground-output").textContent(), /late reply/);
    await page.locator("#playground-dialog button[aria-label=关闭]").click(); assert.equal(await page.locator("#playground-model").inputValue(), ""); assert.equal(await page.locator("#playground-prompt").inputValue(), "");
    await page.locator("#playground-open").click(); await page.locator("#playground-prompt").fill("clear me"); await page.keyboard.press("Escape"); assert.equal(await page.locator("#playground-prompt").inputValue(), "");
    await page.locator("#playground-open").click(); await page.locator("#playground-model").fill("allowed-model"); await page.locator("#playground-prompt").fill("expired session");
    await page.unroute(chatPath); await context.request.delete(base + "/admin/api/session");
    await page.locator("#playground-consume").check(); await page.locator("#playground-send").click();
    await page.locator("#login-panel").waitFor({ state: "visible" });
    assert.equal(await page.locator("#playground-dialog").isVisible(), false);
    assert.equal(await page.locator("#playground-model").inputValue(), "");
    assert.equal(await page.locator("#playground-prompt").inputValue(), "");
  } finally { await context.close(); }
});

test("model call estimates use per-account request pricing, effective mappings and refreshed availability", async () => {
  const context = await browser.newContext({ viewport: { width: 1440, height: 1000 } });
  const page = await context.newPage(); page.on("pageerror", error => failures.push(error.message));
  const future = new Date(Date.now() + 3600000).toISOString(), past = new Date(Date.now() - 3600000).toISOString();
  const models = [
    { id: "alias", billingType: "request", creditsPerRequest: 5 },
    { id: "request", billingType: "request", creditsPerRequest: 100 },
    { id: "token", billingType: "token", creditsPerRequest: 1 },
    { id: "unknown" }, { id: "zero", billingType: "request", creditsPerRequest: 0 },
    { id: "missing", billingType: "request" },
  ];
  let accounts = [
    { id: "one", enabled: true, balance: 160 }, { id: "two", enabled: true, balance: 160 },
    { id: "disabled", enabled: false, balance: 10000 },
    { id: "cooling", enabled: true, balance: 10000, cooldownUntil: future, cooldownReason: "temporary_ban" },
    { id: "error", enabled: true, balance: 10000, error: "unavailable" },
    { id: "recheck", enabled: true, balance: 10000, cooldownUntil: past, cooldownReason: "insufficient_credits" },
    { id: "recovered", enabled: true, balance: 160, cooldownUntil: past, cooldownReason: "temporary_ban" },
    { id: "model-cooling", enabled: true, balance: 10000 },
  ];
  let degraded = false;
  await page.route("**/admin/api/models*", route => route.fulfill({ json: { data: models } }));
  await page.route("**/admin/api/model-policies", route => route.fulfill({ json: { revision: 1, models: models.map(model => ({ sourceId: model.id, publicId: model.id === "alias" ? "public-alias" : model.id, upstreamId: model.id === "alias" ? "request" : model.id, enabled: true })) } }));
  await page.route("**/admin/api/accounts*", route => route.fulfill({ json: { accounts, strategy: "fixed", refreshedAt: new Date().toISOString() } }));
  await page.route("**/admin/api/model-cooldowns", route => route.fulfill({ json: { degraded, entries: [{ accountId: "model-cooling", model: "REQUEST", until: future }] } }));
  const row = id => page.locator("#model-rows tr").filter({ has: page.locator("strong", { hasText: new RegExp("^" + id + "$") }) });
  const refresh = async expected => {
    await page.locator("#analytics-refresh").click();
    await page.waitForFunction(expected => document.querySelector("#model-rows .model-call-estimate")?.textContent === expected, expected);
  };
  try {
    await page.goto(base + "/admin/?view=analytics"); await page.locator("#admin-token").fill("test-admin-123"); await page.locator("#login-submit").click();
    await page.waitForFunction(() => document.querySelector("#model-rows .model-call-estimate")?.textContent === "预计可调用 3 次");
    assert.equal(await row("request").locator(".model-call-estimate").textContent(), "预计可调用 3 次");
    assert.equal(await row("public-alias").locator(".model-credit-value").textContent(), "100 / 次", "aliases use final upstream pricing");
    assert.equal(await row("token").locator(".model-call-estimate").count(), 0);
    assert.equal(await row("unknown").locator(".model-call-estimate").count(), 0);
    for (const id of ["zero", "missing"]) assert.equal(await row(id).locator(".model-call-estimate").textContent(), "预计可调用 -- 次");
    mkdirSync("test-results", { recursive: true });
    for (const width of [1440, 768, 390, 320]) {
      await page.setViewportSize({ width, height: 844 });
      assert.equal(await page.evaluate(() => document.documentElement.scrollWidth > innerWidth), false);
      const estimate = width <= 680 ? page.locator("#model-cards .model-call-estimate").first() : page.locator("#model-rows .model-call-estimate").first();
      assert.equal(await estimate.evaluate(node => {
        const label = node.getBoundingClientRect(), button = node.previousElementSibling.getBoundingClientRect();
        return label.left >= 0 && label.right <= innerWidth && !(label.left < button.right && label.right > button.left && label.top < button.bottom && label.bottom > button.top);
      }), true, "call estimate and test button fit at " + width);
      await page.screenshot({ path: "test-results/model-estimates-" + width + ".png", fullPage: true });
    }
    accounts.push({ id: "unread", enabled: true, balance: null }); await refresh("预计可调用 -- 次");
    accounts = accounts.filter(account => account.id !== "unread").map(account => account.id === "one" ? { ...account, balance: 400 } : account);
    await refresh("预计可调用 6 次");
    degraded = true; await refresh("预计可调用 -- 次");
    degraded = false; accounts = []; await refresh("预计可调用 0 次");
  } finally { await context.close(); }
});

test("settings groups preview impact and persist cooldown threshold, resource TTLs and backup schedule", async () => {
  const context = await browser.newContext({ viewport: { width: 1440, height: 1000 } }); const page = await context.newPage();
  page.on("pageerror", error => failures.push(error.message));
  await page.route("**/admin/api/update**", route => route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ currentVersion: "test", sources: [] }) }));
  await page.goto(base + "/admin/?view=settings"); await page.locator("#admin-token").fill("test-admin-123"); await page.locator("#login-submit").click();
  await page.waitForFunction(() => document.querySelector("#settings-cache-accountsSeconds").value !== "");
  assert.equal(await page.locator("#operations-panel").isHidden(), true);
  assert.equal(await page.locator("#settings-credit-cooldown-threshold").inputValue(), "100");
  await page.locator("#settings-credit-cooldown-threshold").fill("0");
  assert.equal(await page.locator("#settings-credit-cooldown-threshold").evaluate(input => input.checkValidity()), false);
  await page.locator("#settings-credit-cooldown-threshold").fill("200");
  await page.locator("#settings-cache-accountsSeconds").fill("60");
  await page.locator("#settings-cache-modelsSeconds").fill("7200");
  await page.locator("#settings-cache-creditsSeconds").fill("900");
  await page.locator("#settings-server-addr").fill("127.0.0.1:7789");
  await page.waitForFunction(() => document.querySelector("#settings-preview").textContent.includes("待保存 5 项"));
  const preview = await page.locator("#settings-preview").textContent(); assert.match(preview, /保存后生效/); assert.match(preview, /重启后生效/);
  assert.match(preview, /积分冷却阈值 · 保存后生效/);
  await page.locator("#settings-save").click();
  await page.waitForFunction(() => document.querySelector("#settings-preview").hidden && !document.querySelector("#settings-pending").hidden);
  const cache = await (await context.request.get(base + "/admin/api/cache-status")).json();
  assert.equal(cache.accounts.ttlSeconds, 60); assert.equal(cache.models.ttlSeconds, 7200); assert.equal(cache.credits.ttlSeconds, 900);
  const settings = await (await context.request.get(base + "/admin/api/config")).json();
  assert.equal(settings.auth.creditCooldownThreshold, 200);
  assert.equal(settings.fields["auth.creditCooldownThreshold"].mode, "hot");
  assert.equal(settings.fields["auth.creditCooldownThreshold"].effective, 200);
  assert.equal(settings.fields["auth.creditCooldownThreshold"].pending, false);
  await page.locator("#settings-group-storage").click();
  await page.waitForFunction(() => document.querySelector("#backup-schedule-retain").value !== "");
  await page.locator("#backup-schedule-enabled").check(); await page.locator("#backup-schedule-at").fill("03:15"); await page.locator("#backup-schedule-retain").fill("5");
  await page.locator("#backup-schedule-form").getByRole("button", { name: "保存备份计划" }).click();
  await page.waitForFunction(() => document.querySelector("#backup-schedule-status").textContent.includes("每日 03:15"));
  await page.reload(); await page.locator("#settings-group-storage").click();
  await page.waitForFunction(() => document.querySelector("#backup-schedule-enabled").checked);
  assert.equal(await page.locator("#backup-schedule-retain").inputValue(), "5");
  await page.locator("#backup-schedule-enabled").uncheck(); await page.locator("#backup-schedule-form").getByRole("button", { name: "保存备份计划" }).click();
  await page.waitForFunction(() => document.querySelector("#backup-schedule-status").textContent.includes("已关闭"));
  await page.locator("#settings-group-general").click();
  assert.equal(await page.locator("#settings-credit-cooldown-threshold").inputValue(), "200");
  for (const width of [1440, 768, 390, 320]) {
    await page.setViewportSize({ width, height: 844 }); assert.equal(await page.evaluate(() => document.documentElement.scrollWidth > innerWidth), false);
    await page.locator("#settings-credit-cooldown-threshold").scrollIntoViewIfNeeded();
    const bounds = await page.locator("#settings-credit-cooldown-threshold").boundingBox();
    assert.ok(bounds.x >= 0 && bounds.x + bounds.width <= width);
    await page.screenshot({ path: "test-results/settings-groups-" + width + ".png", fullPage: true });
  }
  await context.close();
});

test("operations exposes local degradation, preserves prior status and clears on logout", async () => {
  const context = await browser.newContext({ viewport: { width: 390, height: 844 } });
  const page = await context.newPage();
  page.on("pageerror", error => failures.push(error.message));
  let unavailable = false;
  await page.route("**/admin/api/update**", route => route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ currentVersion: "test", sources: [] }) }));
  await page.route("**/admin/api/system-status", route => route.fulfill({ status: unavailable ? 503 : 200, contentType: "application/json", body: JSON.stringify(unavailable ? { error: "database unavailable" } : { ready: true, database: true, admission: true, active: 2, jobsAvailable: true, queuedJobs: 3, runningJobs: 1, failedJobs: 4, audit: { degraded: true, queueLength: 2, dropped: 7, failures: 1 }, accounts: { total: 4, enabled: 3, available: 2, cooling: 1 }, startedAt: new Date().toISOString(), checkedAt: new Date().toISOString() }) }));
  await page.goto(base + "/admin/?view=settings");
  await page.locator("#admin-token").fill("test-admin-123");
  await page.locator("#login-submit").click();
  await page.locator("#settings-group-operations").click();
  await page.waitForFunction(() => document.querySelector("#operations-fields").textContent.includes("存储降级"));
  await page.waitForFunction(() => document.querySelector("#login-events").textContent.includes("登录成功"));
  assert.match(await page.locator("#operations-fields").textContent(), /丢弃 7/);
  assert.equal(await page.locator("#service-state span").textContent(), "部分功能异常");
  unavailable = true;
  await page.locator("#operations-refresh").click();
  await page.waitForFunction(() => document.querySelector("#operations-error").textContent.includes("database unavailable"));
  assert.match(await page.locator("#operations-fields").textContent(), /丢弃 7/);
  unavailable = false;
  await page.locator("#operations-refresh").click();
  await page.waitForFunction(() => document.querySelector("#operations-error").textContent === "");
  for (const width of [1440, 768, 390, 320]) {
    await page.setViewportSize({ width, height: 844 });
    assert.equal(await page.evaluate(() => document.documentElement.scrollWidth > innerWidth), false);
    await page.screenshot({ path: "test-results/operations-" + width + ".png", fullPage: true });
  }
  await page.locator("#logout-button").click();
  await page.locator("#login-panel").waitFor({ state: "visible" });
  assert.equal(await page.locator("#operations-fields").textContent(), "");
  assert.equal(await page.locator("#login-events").textContent(), "");
  await context.close();
});

test("admin client enforces timeouts and cancellation and ignores late unauthorized replies", async () => {
  const context = await browser.newContext(); const page = await context.newPage();
  await page.goto(base + "/admin/");
  const result = await page.evaluate(async () => {
    const original = window.fetch;
    let unauthorized = 0, settle;
    const client = window.createAdminClient({ onUnauthorized: () => unauthorized++ });
    window.fetch = (path, options) => new Promise((resolve, reject) => { options.signal.addEventListener("abort", () => reject(options.signal.reason), { once: true }); });
    const timeout = await client.request("/timeout", { timeout: 5 }).catch(error => error.name);
    const controller = new AbortController();
    const pending = client.request("/cancel", { signal: controller.signal }).catch(error => error.name); controller.abort();
    const cancelled = await pending;
    window.fetch = () => new Promise(resolve => { settle = resolve; });
    const late = client.request("/late").catch(error => error.name); client.clear();
    settle(new Response('{"error":"expired"}', { status: 401, headers: { "Content-Type": "application/json" } }));
    const ignored = await late; window.fetch = original;
    return { timeout, cancelled, ignored, unauthorized };
  });
  assert.deepEqual(result, { timeout: "TimeoutError", cancelled: "AbortError", ignored: "AbortError", unauthorized: 0 });
  await context.close();
});

test("task center restores progress, cancels work and restricts unsafe retries", async () => {
  const context = await browser.newContext({ viewport: { width: 1440, height: 1000 } });
  const page = await context.newPage();
  page.on("pageerror", error => failures.push(error.message));
  const now = new Date().toISOString();
  let unavailable = false;
  const jobs = [
    { id: "job-active", kind: "probe", state: "running", items: [{ target: "fixture", state: "running" }], createdAt: now, updatedAt: now },
    { id: "job-failed", kind: "refresh", state: "partial", items: [{ target: "fixture", state: "failed", error: "operation_failed" }], createdAt: now, updatedAt: now },
    { id: "job-redeem", kind: "redeem", state: "interrupted", items: [{ target: "fixture", state: "interrupted", error: "result_unknown" }], createdAt: now, updatedAt: now },
  ];
  await page.route("**/admin/api/update**", route => route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ currentVersion: "test", sources: [] }) }));
  await page.route("**/admin/api/jobs**", route => {
    const path = new URL(route.request().url()).pathname;
    if (unavailable) return route.fulfill({ status: 503, contentType: "application/json", body: JSON.stringify({ error: "job storage unavailable" }) });
    if (path.endsWith("/cancel")) { jobs[0] = { ...jobs[0], state: "cancelled", items: [{ target: "fixture", state: "cancelled" }] }; return route.fulfill({ status: 204 }); }
    if (path.endsWith("/retry")) {
      const job = { ...jobs[1], id: "job-retried", state: "queued", items: [{ target: "fixture", state: "queued" }] };
      jobs.push(job);
      return route.fulfill({ status: 202, contentType: "application/json", body: JSON.stringify({ job }) });
    }
    return route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ jobs }) });
  });
  await page.goto(base + "/admin/");
  await page.locator("#admin-token").fill("test-admin-123");
  await page.locator("#login-submit").click();
  await page.locator("#jobs-open").click();
  await page.waitForFunction(() => document.querySelectorAll("#jobs-list .job-item").length === 3);
  const active = page.locator("[data-job-id=job-active]");
  await active.locator("summary").click();
  await active.getByRole("button", { name: "取消任务", exact: true }).click();
  await page.waitForFunction(() => document.querySelector("[data-job-id=job-active]").textContent.includes("已取消"));
  const failed = page.locator("[data-job-id=job-failed]");
  await failed.locator("summary").click();
  await failed.getByRole("button", { name: "重试未完成项" }).click();
  await page.locator("[data-job-id=job-retried]").waitFor();
  assert.equal(await page.locator("[data-job-id=job-redeem] button").count(), 0);
  await page.reload();
  await page.locator("#jobs-open").click();
  await page.locator("[data-job-id=job-retried]").waitFor();
  await page.locator("#jobs-state").selectOption("active");
  assert.equal(await page.locator("#jobs-list .job-item").count(), 1);
  await page.locator("#jobs-state").selectOption("");
  unavailable = true;
  await page.locator("#jobs-refresh").click();
  await page.waitForFunction(() => document.querySelector("#jobs-error").textContent.includes("读取失败"));
  unavailable = false;
  await page.locator("#jobs-refresh").click();
  await page.waitForFunction(() => document.querySelector("#jobs-error").textContent === "");
  for (const width of [1440, 768, 390, 320]) {
    await page.setViewportSize({ width, height: 844 });
    assert.equal(await page.evaluate(() => document.documentElement.scrollWidth > innerWidth), false);
    assert.equal(await page.locator("#jobs-dialog").evaluate(node => node.scrollWidth > node.clientWidth), false);
    await page.screenshot({ path: "test-results/task-center-" + width + ".png", fullPage: true });
  }
  await context.close();
});

test("request diagnosis links model mapping, attempts and current cooldown", async () => {
  const context = await browser.newContext({ viewport: { width: 1440, height: 1000 } });
  const page = await context.newPage();
  page.on("pageerror", error => failures.push(error.message));
  await page.route("**/admin/api/update**", route => route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ currentVersion: "test", sources: [] }) }));
  await page.goto(base + "/admin/");
  await page.locator("#admin-token").fill("test-admin-123");
  await page.locator("#login-submit").click();
  await page.locator("#dashboard").waitFor({ state: "visible" });
  const key = await (await context.request.post(base + "/admin/api/keys", { data: { name: "diagnostic-test" } })).json();
  const response = await context.request.post(base + "/v1/chat/completions", { headers: { Authorization: "Bearer " + key.secret }, data: { model: "allowed-model", messages: [{ role: "user", content: "hello" }] } });
  assert.equal(response.status(), 200);
  const requestId = response.headers()["x-request-id"];
  await selectView(page, "records-tab");
  await page.locator("#request-filter-key").fill(key.key.id);
  await page.locator("#request-record-filters").getByRole("button", { name: "应用筛选" }).click();
  await page.waitForFunction(() => document.querySelectorAll("#request-record-rows tr").length === 1);
  await page.locator("#request-record-rows").getByRole("button", { name: "诊断请求 " + requestId, exact: true }).click();
  await page.waitForFunction(() => document.querySelector(".diagnostic-cooldowns").textContent.includes("没有匹配"));
  const diagnostic = await page.locator("#diagnostic-content").textContent();
  assert.match(diagnostic, /上游尝试/);
  assert.equal(diagnostic.includes(requestId), true);
  assert.equal(diagnostic.includes("allowed-model"), true);
  assert.equal(diagnostic.includes(key.secret), false);
  for (const width of [1440, 768, 390, 320]) {
    await page.setViewportSize({ width, height: 844 });
    assert.equal(await page.evaluate(() => document.documentElement.scrollWidth > innerWidth), false);
    assert.equal(await page.locator("#diagnostic-dialog").evaluate(node => node.scrollWidth > node.clientWidth), false);
    await page.screenshot({ path: "test-results/diagnostics-" + width + ".png", fullPage: true });
  }
  await page.locator("[data-close=diagnostic-dialog]").last().click();
  assert.equal(await page.locator("#diagnostic-content").textContent(), "");
  await context.request.delete(base + "/admin/api/keys/" + key.key.id + "?version=" + key.key.version);
  await context.close();
});

test("analytics preserves successful modules and retries only the failed resource", async () => {
  const context = await browser.newContext({ viewport: { width: 1440, height: 1000 } });
  const page = await context.newPage();
  page.on("pageerror", error => failures.push(error.message));
  await page.route("**/admin/api/update**", route => route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ currentVersion: "test", sources: [] }) }));
  let unavailable = true, modelReads = 0;
  await page.route("**/admin/api/models**", route => { modelReads++; return route.continue(); });
  await page.route("**/admin/api/credit-usage**", route => unavailable ? route.fulfill({ status: 503, contentType: "application/json", body: JSON.stringify({ error: "credit service unavailable" }) }) : route.continue());
  await page.goto(base + "/admin/?view=analytics");
  await page.locator("#admin-token").fill("test-admin-123");
  await page.locator("#login-submit").click();
  await page.locator("#credit-usage-error").waitFor({ state: "visible" });
  await page.waitForFunction(() => document.querySelectorAll("#model-rows tr").length === 2 && document.querySelector("#usage-total").textContent !== "--");
  assert.equal(await page.locator("#model-error").isHidden(), true);
  const count = modelReads;
  unavailable = false;
  await page.locator("#credit-usage-error").getByRole("button", { name: "重试" }).click();
  await page.locator("#credit-usage-error").waitFor({ state: "hidden" });
  assert.equal(modelReads, count);
  await page.route("**/admin/api/models**", route => route.fulfill({ status: 503, contentType: "application/json", body: JSON.stringify({ error: "catalog unavailable" }) }));
  await page.locator("#analytics-refresh").click();
  await page.locator("#model-error").waitFor({ state: "visible" });
  assert.equal(await page.locator("#model-rows tr").count(), 2);
  assert.match(await page.locator("#model-error + .resource-status").textContent(), /上次成功/);
  await context.close();
});

test("URL restores request filters and pagination across reload and browser history", async () => {
  const context = await browser.newContext({ viewport: { width: 1440, height: 1000 } });
  const page = await context.newPage();
  page.on("pageerror", error => failures.push(error.message));
  await page.route("**/admin/api/update**", route => route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ currentVersion: "test", sources: [] }) }));
  await page.goto(base + "/admin/");
  await page.locator("#admin-token").fill("test-admin-123");
  await page.locator("#login-submit").click();
  await page.locator("#dashboard").waitFor({ state: "visible" });
  const created = await (await context.request.post(base + "/admin/api/keys", { data: { name: "navigation-test" } })).json();
  for (let i = 0; i < 23; i++) {
    assert.equal((await context.request.post(base + "/v1/chat/completions", { headers: { Authorization: "Bearer " + created.secret }, data: { model: "allowed-model", messages: [] } })).status(), 200);
  }
  await selectView(page, "records-tab");
  await page.locator("#request-filter-key").fill(created.key.id);
  await page.locator("#request-filter-model").fill("allowed-model");
  await page.locator("#request-record-filters").getByRole("button", { name: "应用筛选" }).click();
  await page.waitForFunction(() => document.querySelector("#request-record-count").textContent.startsWith("23 条"));
  await page.locator("#request-next-page").click();
  await page.waitForFunction(() => document.querySelector("#request-page-label").textContent.includes("2 / 2"));
  assert.equal(new URL(page.url()).searchParams.get("page"), "2");
  await page.reload();
  await page.waitForFunction(() => document.querySelector("#request-page-label").textContent.includes("2 / 2"));
  assert.equal(await page.locator("#request-filter-key").inputValue(), created.key.id);
  assert.equal(await page.locator("#request-filter-model").inputValue(), "allowed-model");
  await selectView(page, "accounts-tab");
  await page.goBack();
  await page.locator("#records-view").waitFor({ state: "visible" });
  assert.match(await page.locator("#request-page-label").textContent(), /2 \/ 2/);
  await page.goForward();
  await page.locator("#accounts-view").waitFor({ state: "visible" });
  assert.equal(page.url().includes(created.secret), false);
  await context.request.delete(base + "/admin/api/keys/" + created.key.id + "?version=" + created.key.version);
  await context.close();
});

test("account filters, selection scope and atomic batch enable survive reload", async () => {
  const context = await browser.newContext({ viewport: { width: 1440, height: 1000 } });
  const page = await context.newPage();
  page.on("pageerror", error => failures.push(error.message));
  await page.route("**/admin/api/update**", route => route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ currentVersion: "test", sources: [] }) }));
  await page.goto(base + "/admin/");
  await page.locator("#admin-token").fill("test-admin-123");
  await page.locator("#login-submit").click();
  await page.locator("#dashboard").waitFor({ state: "visible" });
  const account = await (await context.request.post(base + "/admin/api/accounts", { data: { identifier: "batch-test@example.test", password: "fixture-password" } })).json();
  try {
    await context.request.patch(base + "/admin/api/accounts/" + account.id, { data: { enabled: false } });
    await page.locator("#refresh-button").click();
    await page.waitForFunction(() => document.querySelectorAll("#account-rows tr").length === 2);
    await page.locator("#account-status").selectOption("disabled");
    await page.locator("#account-search").fill("batch-test");
    assert.equal(await page.locator("#account-rows tr").count(), 1);
    await page.reload();
    await page.waitForFunction(() => document.querySelectorAll("#account-rows tr").length === 1);
    assert.equal(await page.locator("#account-status").inputValue(), "disabled");
    assert.equal(await page.locator("#account-search").inputValue(), "batch-test");
    assert.equal(await page.locator("#account-batch").isHidden(), true);
    await page.locator("#select-all").check();
    await page.locator("#account-batch-action").selectOption("enable");
    await page.locator("#account-batch").getByRole("button", { name: "执行", exact: true }).click();
    await page.locator("#account-filter-empty").waitFor({ state: "visible" });
    assert.match(await page.locator("#account-selected-count").textContent(), /筛选外 1/);
    await page.locator("#account-status").selectOption("all");
    await page.locator("#account-batch-action").selectOption("checkin");
    const submitted = page.waitForResponse(response => response.url().endsWith("/admin/api/jobs") && response.request().method() === "POST");
    await page.locator("#account-batch").getByRole("button", { name: "执行", exact: true }).click();
    const job = (await (await submitted).json()).job;
    assert.deepEqual(job.items.map(item => item.target), [account.id]);
    for (const width of [1440, 768, 390, 320]) {
      await page.setViewportSize({ width, height: 844 });
      assert.equal(await page.evaluate(() => document.documentElement.scrollWidth > innerWidth), false, "account filter overflow at " + width);
      await page.screenshot({ path: "test-results/account-tools-" + width + ".png", fullPage: true });
    }
  } finally { await context.request.delete(base + "/admin/api/accounts/" + account.id); await context.close(); }
});

test("connection templates keep one-time secrets only until close or logout", async () => {
  const context = await browser.newContext({ viewport: { width: 390, height: 844 } });
  const page = await context.newPage();
  page.on("pageerror", error => failures.push(error.message));
  await page.route("**/admin/api/update**", route => route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ currentVersion: "test", sources: [] }) }));
  await page.goto(base + "/admin/?view=keys");
  await page.locator("#admin-token").fill("test-admin-123");
  await page.locator("#login-submit").click();
  await page.locator("#keys-create").click();
  await page.locator("#key-name").fill("connection-test");
  await page.locator("#key-save").click();
  await page.locator("#key-secret-dialog").waitFor({ state: "visible" });
  const secret = await page.locator("#key-secret").inputValue();
  const key = (await (await context.request.get(base + "/admin/api/keys")).json()).keys.find(item => item.name === "connection-test");
  await page.locator("#key-connect").click();
  await page.waitForFunction(() => document.querySelector("#connection-model").value === "allowed-model");
  assert.equal(await page.locator("#key-secret").inputValue(), "");
  assert.equal((await page.locator("#connection-output").inputValue()).includes(secret), true);
  await page.locator("#connection-format").selectOption("python");
  assert.match(await page.locator("#connection-output").inputValue(), /from openai import OpenAI/);
  await page.locator("#connection-format").selectOption("client");
  assert.deepEqual(JSON.parse(await page.locator("#connection-output").inputValue()), { base_url: base + "/v1", api_key: secret, model: "allowed-model", endpoint: "/v1/chat/completions" });
  for (const width of [1440, 768, 390, 320]) {
    await page.setViewportSize({ width, height: 844 });
    assert.equal(await page.evaluate(() => document.documentElement.scrollWidth > innerWidth), false);
    await page.screenshot({ path: "test-results/connections-" + width + ".png", fullPage: true });
  }
  await page.locator("[data-close=connection-dialog]").last().click();
  assert.equal(await page.locator("#connection-output").inputValue(), "");
  await page.locator("#connections-open").click();
  assert.equal((await page.locator("#connection-output").inputValue()).includes(secret), false);
  await page.evaluate(async () => { await fetch("/admin/api/session", { method: "DELETE" }); });
  await page.locator("#connection-dialog").getByRole("button", { name: "完成", exact: true }).click();
  await page.locator("#keys-refresh").click();
  await page.locator("#login-panel").waitFor({ state: "visible" });
  assert.equal(await page.locator("#connection-output").inputValue(), "");
  assert.equal(page.url().includes(secret), false);
  assert.deepEqual(await page.evaluate(() => [localStorage.length, sessionStorage.length]), [0, 0]);
  await page.locator("#admin-token").fill("test-admin-123");
  await page.locator("#login-submit").click();
  await page.locator("#dashboard").waitFor({ state: "visible" });
  await context.request.delete(base + "/admin/api/keys/" + key.id + "?version=" + key.version);
  await context.close();
});

test("Base URL, generated keys and connection templates reach the real clipboard with modern and fallback copying", async () => {
  for (const mode of ["modern", "missing", "denied"]) {
    const context = await browser.newContext({ viewport: { width: 390, height: 844 }, permissions: ["clipboard-read", "clipboard-write"] });
    const page = await context.newPage();
    page.on("pageerror", error => failures.push(error.message));
    await page.addInitScript(mode => {
      const clipboard = navigator.clipboard;
      window.readClipboard = () => clipboard.readText();
      if (mode !== "modern") Object.defineProperty(navigator, "clipboard", { value: mode === "missing" ? undefined : { writeText: async () => { throw new Error("denied"); } } });
    }, mode);
    await page.route("**/admin/api/update**", route => route.fulfill({ contentType: "application/json", body: JSON.stringify({ currentVersion: "test", sources: [] }) }));
    let key;
    try {
      await page.goto(base + "/admin/?view=keys");
      await page.locator("#admin-token").fill("test-admin-123");
      await page.locator("#login-submit").click();
      await page.locator("#keys-base-copy").click();
      await page.waitForFunction(() => document.querySelector("#toast").textContent === "Base URL 已复制");
      assert.equal(await page.locator("#keys-base-url").textContent(), base + "/v1");
      assert.equal(await page.evaluate(() => window.readClipboard()), base + "/v1", mode);
      await page.locator("#keys-create").click();
      await page.locator("#key-name").fill("clipboard-" + mode);
      await page.locator("#key-save").click();
      await page.locator("#key-secret-dialog").waitFor({ state: "visible" });
      const secret = await page.locator("#key-secret").inputValue();
      key = (await (await context.request.get(base + "/admin/api/keys")).json()).keys.find(item => item.name === "clipboard-" + mode);
      await page.locator("#key-copy").click();
      await page.waitForFunction(() => document.querySelector("#toast").textContent === "密钥已复制");
      assert.equal(await page.evaluate(() => window.readClipboard()), secret, mode + " inside modal");
      assert.equal(await page.locator("#key-copy-error").isVisible(), false);
      await page.locator("#key-connect").click();
      await page.waitForFunction(() => document.querySelector("#connection-model").value === "allowed-model");
      await page.locator("#connection-copy").click();
      await page.waitForFunction(() => document.querySelector("#toast").textContent === "接入配置已复制");
      assert.equal(await page.evaluate(() => window.readClipboard()), await page.locator("#connection-output").inputValue());
      assert.equal(await page.locator(".clipboard-buffer").count(), 0);
      await page.keyboard.press("Escape");
      assert.equal(await page.locator("#key-secret").inputValue(), "");
      assert.equal(await page.locator("#connection-output").inputValue(), "");
      mkdirSync("test-results", { recursive: true });
      if (mode === "missing") for (const width of [1440, 768, 390, 320]) {
        await page.setViewportSize({ width, height: 844 });
        assert.equal(await page.evaluate(() => document.documentElement.scrollWidth > innerWidth), false);
        assert.equal(await page.locator(".api-base").evaluate(node => [...node.querySelectorAll("button, code")].some(item => item.scrollWidth > item.clientWidth + 1)), false);
        await page.screenshot({ path: "test-results/keys-base-url-" + width + ".png", fullPage: true });
      }
    } finally {
      if (key) await context.request.delete(base + "/admin/api/keys/" + key.id + "?version=" + key.version);
      await context.close();
    }
  }
});

test("model types guide connection endpoints and playground modes across desktop and mobile", async () => {
  const context = await browser.newContext({ viewport: { width: 1440, height: 1000 } });
  const page = await context.newPage();
  page.on("pageerror", error => failures.push(error.message));
  const catalog = [
    { id: "gpt-image-2" }, { id: "custom-painter", type: "text_to_image" },
    { id: "retouch", supported_endpoints: ["/v1/images/edits"] },
    { id: "qwen-vl", input_modalities: ["image", "text"], output_modalities: ["text"] },
    { id: "mapped-source", type: "chat" }, { id: "unlisted" },
  ];
  const policies = catalog.map(model => ({ sourceId: model.id, publicId: model.id === "mapped-source" ? "studio" : model.id, upstreamId: model.id === "mapped-source" ? "retouch" : model.id, enabled: true, endpoints: model.id === "retouch" ? ["/v1/images/edits"] : [] }));
  await page.route("**/admin/api/models", route => route.fulfill({ contentType: "application/json", body: JSON.stringify({ data: catalog }) }));
  await page.route("**/admin/api/model-policies", route => route.fulfill({ contentType: "application/json", body: JSON.stringify({ revision: 1, models: policies }) }));
  await page.route("**/admin/api/update**", route => route.fulfill({ contentType: "application/json", body: JSON.stringify({ currentVersion: "test", sources: [] }) }));
  try {
    await page.goto(base + "/admin/?view=analytics");
    await page.locator("#admin-token").fill("test-admin-123");
    await page.locator("#login-submit").click();
    await page.waitForFunction(() => document.querySelectorAll("#model-rows tr").length === 6);
    const expected = ["文生图 / 图生图", "文生图", "图生图", "对话", "图生图", "类型未知"];
    assert.deepEqual(await page.locator("#model-rows .model-types").allTextContents(), expected);
    assert.deepEqual(await page.locator("#model-cards .model-types").allTextContents(), expected);
    await page.locator("#model-rows tr").first().getByRole("button", { name: "测试", exact: true }).click();
    assert.equal(await page.locator("#playground-mode-image").isChecked(), true);
    await page.keyboard.press("Escape");
    mkdirSync("test-results", { recursive: true });
    for (const width of [1440, 768, 390, 320]) {
      await page.setViewportSize({ width, height: 844 });
      assert.equal(await page.evaluate(() => document.documentElement.scrollWidth > innerWidth), false);
      await page.screenshot({ path: "test-results/model-types-" + width + ".png", fullPage: true });
    }
    await page.setViewportSize({ width: 1440, height: 1000 });
    await selectView(page, "keys-tab");
    await page.locator("#connections-open").click();
    await page.waitForFunction(() => document.querySelector("#connection-model").value === "gpt-image-2");
    assert.equal(await page.locator("#connection-endpoint").inputValue(), "image");
    assert.match(await page.locator("#connection-output").inputValue(), /\/v1\/images\/generations/);
    assert.equal(await page.locator("#connection-endpoint option[value=chat]").evaluate(node => node.disabled), true);
    await page.locator("#connection-endpoint").selectOption("edit");
    let output = await page.locator("#connection-output").inputValue();
    assert.match(output, /\/v1\/images\/edits/);
    assert.match(output, /-F 'image\[\]=@input.png'/);
    assert.equal(output.includes("application/json"), false);
    await page.locator("#connection-format").selectOption("python");
    output = await page.locator("#connection-output").inputValue();
    assert.match(output, /with open\("input.png", "rb"\) as image:/);
    assert.match(output, /client.images.edit\(/);
    await page.locator("#connection-model").selectOption("studio");
    assert.equal(await page.locator("#connection-endpoint").inputValue(), "edit");
    assert.match(await page.locator("#connection-output").inputValue(), /model="studio"/);
    await page.locator("#connection-model").selectOption("custom-painter");
    assert.equal(await page.locator("#connection-endpoint").inputValue(), "image");
    assert.match(await page.locator("#connection-output").inputValue(), /client.images.generate\(/);
    await page.locator("#connection-model").selectOption("qwen-vl");
    assert.equal(await page.locator("#connection-endpoint").inputValue(), "chat");
    await page.locator("#connection-endpoint").selectOption("responses");
    assert.match(await page.locator("#connection-output").inputValue(), /client.responses.create\(/);
    await page.locator("#connection-format").selectOption("curl");
    output = await page.locator("#connection-output").inputValue();
    assert.match(output, /\/v1\/responses/);
    assert.deepEqual(JSON.parse(output.slice(output.indexOf("-d '") + 4, -1)), { model: "qwen-vl", input: "你好" });
    await page.locator("#connection-format").selectOption("client");
    assert.equal(JSON.parse(await page.locator("#connection-output").inputValue()).endpoint, "/v1/responses");
    await page.locator("#connection-model").selectOption("unlisted");
    assert.equal(await page.locator("#connection-endpoint option:disabled").count(), 0, "unknown types retain manual endpoint selection");
    for (const width of [1440, 768, 390, 320]) {
      await page.setViewportSize({ width, height: 844 });
      assert.equal(await page.locator("#connection-dialog").evaluate(node => node.scrollWidth > node.clientWidth + 1), false);
      await page.screenshot({ path: "test-results/connection-types-" + width + ".png" });
    }
    assert.deepEqual(failures, []);
  } finally { await context.close(); }
});

test("shared admission settings, request quota and background account actions", async () => {
  const context = await browser.newContext({ viewport: { width: 1440, height: 1000 } });
  const page = await context.newPage();
  page.on("pageerror", error => failures.push(error.message));
  await page.route("**/admin/api/update**", route => route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ currentVersion: "test", sources: [] }) }));
  await page.goto(base + "/admin/");
  await page.locator("#admin-token").fill("test-admin-123");
  await page.locator("#login-submit").click();
  await page.locator("#dashboard").waitFor({ state: "visible" });
  await selectView(page, "settings-tab");
  await page.waitForFunction(() => document.querySelector("#admission-maxConcurrent").value !== "");
  await page.waitForFunction(() => document.querySelector("#settings-account-rpm").value !== "");
  await page.locator("#settings-account-rpm").fill("5");
  await page.locator("#settings-server-addr").fill("127.0.0.1:7788");
  await page.locator("#settings-save").click();
  await page.waitForFunction(() => document.querySelector("#settings-server-addr").closest("label").textContent.includes("待重启"));
  const settings = await (await context.request.get(base + "/admin/api/config")).json();
  assert.equal(settings.auth.rpm, 5);
  assert.equal(settings.fields["server.addr"].effective, "127.0.0.1:0");
  assert.equal(settings.fields["auth.rpm"].mode, "hot");
  assert.equal(settings.redemption.code, "");
  const stale = await context.request.put(base + "/admin/api/config", { data: { ...settings, revision: settings.revision - 1 } });
  assert.equal(stale.status(), 409);
  await page.locator("#settings-group-admission").click();
  await page.locator("#admission-maxConcurrent").fill("20");
  await page.getByRole("button", { name: "保存准入设置", exact: true }).click();
  await page.waitForFunction(() => document.querySelector("#admission-maxConcurrent").value === "20" && !document.querySelector("#admission-maxConcurrent").form.querySelector("button").disabled);
  const limits = await (await context.request.get(base + "/admin/api/admission")).json();
  assert.equal(limits.settings.maxConcurrent, 20);
  await page.locator("#settings-group-storage").click();
  await page.getByRole("button", { name: "创建完整备份", exact: true }).click();
  await page.locator("#storage-panel a[download]").first().waitFor();
  const archive = await context.request.get(base + await page.locator("#storage-panel a[download]").first().getAttribute("href"));
  assert.equal(archive.status(), 200);
  const archiveBytes = await archive.body();
  await page.locator("#backup-upload").setInputFiles({ name: "restore.zip", mimeType: "application/zip", buffer: archiveBytes });
  await page.waitForFunction(() => document.querySelector("#backup-validation").textContent.includes("已验证"));
  page.once("dialog", dialog => dialog.accept());
  await page.locator("#storage-panel .backup-delete").first().click();
  await page.waitForFunction(() => document.querySelectorAll("#storage-panel .backup-item").length === 0);
  for (const width of [1440, 768, 390, 320]) {
    await page.setViewportSize({ width, height: 1000 });
    await page.locator("#storage-panel").scrollIntoViewIfNeeded();
    await page.screenshot({ path: "test-results/storage-" + width + ".png", fullPage: true });
    assert.equal(await page.evaluate(() => document.documentElement.scrollWidth > innerWidth), false);
  }
  await page.setViewportSize({ width: 1440, height: 1000 });
  await selectView(page, "keys-tab");
  await page.locator("#keys-create").click();
  await page.locator("#key-name").fill("Quota browser key");
  await page.locator("#key-concurrent").fill("1");
  await page.locator("#key-daily").fill("1");
  await page.locator("#key-save").click();
  await page.locator("#key-secret-dialog").waitFor({ state: "visible" });
  const secret = await page.locator("#key-secret").inputValue();
  await page.locator("[data-close='key-secret-dialog']").last().click();
  const infer = () => context.request.post(base + "/v1/chat/completions", { headers: { Authorization: "Bearer " + secret }, data: { model: "allowed-model", messages: [] } });
  assert.equal((await infer()).status(), 200);
  assert.equal((await infer()).status(), 429);
  await page.locator("#keys-refresh").click();
  await page.waitForFunction(() => document.querySelector("#keys-rows").textContent.includes("日请求 1 / 1"));
  const key = (await (await context.request.get(base + "/admin/api/keys")).json()).keys.find(item => item.name === "Quota browser key");
  await context.request.delete(base + "/admin/api/keys/" + key.id + "?version=" + key.version);
  await selectView(page, "accounts-tab");
  const submitted = page.waitForResponse(response => response.url().endsWith("/admin/api/jobs") && response.request().method() === "POST");
  await page.locator("#checkin-all-button").click();
  const created = await (await submitted).json();
  assert.equal(created.job.kind, "checkin");
  await page.waitForFunction(async id => {
    const response = await fetch("/admin/api/jobs");
    const result = await response.json();
    return result.jobs.some(job => job.id === id && !["queued", "running"].includes(job.state));
  }, created.job.id);
  await page.locator("#redeem-open").click();
  await page.locator("#redemption-code").fill("test-code");
  await page.locator("#select-enabled").click();
  let redemptionStarted = false;
  let redemptionPolls = 0;
  let releaseFirstRedemptionPoll;
  let signalFirstRedemptionPoll;
  const firstRedemptionPollStarted = new Promise(resolve => { signalFirstRedemptionPoll = resolve; });
  const holdFirstRedemptionPoll = new Promise(resolve => { releaseFirstRedemptionPoll = resolve; });
  await page.route("**/admin/api/jobs", async route => {
    if (!redemptionStarted) return route.continue();
    if (route.request().method() === "POST") {
      await route.fulfill({ status: 202, contentType: "application/json", body: JSON.stringify({ job: { id: "redemption-progress-test", kind: "redeem", state: "queued", items: [{ target: "fixture", state: "queued" }, { target: "fixture-2", state: "queued" }] } }) });
      return;
    }
    redemptionPolls += 1;
    if (redemptionPolls === 1) {
      signalFirstRedemptionPoll();
      await holdFirstRedemptionPoll;
      await route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ jobs: [{ id: "redemption-progress-test", kind: "redeem", state: "running", items: [
        { target: "fixture", state: "succeeded", result: { account: "fixture@example.test", status: "success", message: "到账 500 积分", credits: 500 } },
        { target: "fixture-2", state: "running" },
      ] }] }) });
      return;
    }
    await route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ jobs: [{ id: "redemption-progress-test", kind: "redeem", state: "succeeded", items: [
      { target: "fixture", state: "succeeded", result: { account: "fixture@example.test", status: "success", message: "到账 500 积分", credits: 500 } },
      { target: "fixture-2", state: "succeeded", result: { account: "second@example.test", status: "success", message: "到账 600 积分", credits: 600 } },
    ] }] }) });
  });
  redemptionStarted = true;
  await page.locator("#redeem-submit").click();
  await page.locator("#result-dialog").waitFor({ state: "visible" });
  assert.equal(await page.locator("#result-title").textContent(), "兑换处理中");
  assert.match(await page.locator("#result-list").textContent(), /等待兑换/);
  await firstRedemptionPollStarted;
  releaseFirstRedemptionPoll();
  await page.waitForFunction(() => document.querySelector("#result-summary").textContent.includes("成功 1 个"));
  assert.match(await page.locator("#result-summary").textContent(), /已完成 1 \/ 2 个账号/);
  assert.match(await page.locator("#result-list").textContent(), /到账 500 积分.*正在兑换/);
  await page.waitForFunction(() => document.querySelector("#result-summary").textContent.includes("成功 2 \/ 2 个账号"));
  assert.match(await page.locator("#result-list").textContent(), /到账 600 积分/);
  await page.locator("[data-close='result-dialog']").last().click();
  assert.equal(await page.locator("#jobs-panel").count(), 0);
  await page.reload();
  await page.locator("#dashboard").waitFor({ state: "visible" });
  assert.equal(await page.locator("#jobs-panel").count(), 0);
  const jobs = (await (await context.request.get(base + "/admin/api/jobs")).json()).jobs;
  assert.ok(jobs.some(job => job.kind === "checkin"));
  for (const width of [1440, 768, 390, 320]) {
    await page.setViewportSize({ width, height: 1000 });
    await page.screenshot({ path: "test-results/jobs-" + width + ".png", fullPage: true });
    assert.equal(await page.evaluate(() => document.documentElement.scrollWidth > innerWidth), false);
  }
  assert.deepEqual(failures, []);
  await context.close();
});
before(async () => {
  fixture = spawn("go", ["run", "./tests/webfixture"], { detached: true, stdio: ["ignore", "pipe", "pipe"] });
  let output = "";
  base = await new Promise((resolve, reject) => {
    const timer = setTimeout(() => reject(new Error("Fixture startup timed out: " + output)), 30000);
    fixture.stdout.on("data", data => {
      output += data;
      const match = output.match(/WEB_FIXTURE_URL=(http:\/\/\S+)/);
      if (match) { clearTimeout(timer); resolve(match[1]); }
    });
    fixture.stderr.on("data", data => { output += data; });
    fixture.on("exit", code => { clearTimeout(timer); reject(new Error("Fixture exited: " + code + " " + output)); });
  });
  const executablePath = process.env.CHROMIUM_PATH || ["/data/data/com.termux/files/usr/bin/chromium-browser", "/usr/bin/chromium", "/usr/bin/google-chrome"].find(existsSync);
  browser = await chromium.launch({ executablePath, headless: true, args: ["--no-sandbox", "--disable-dev-shm-usage", "--disable-gpu"] });
});
after(async () => {
  if (browser) await browser.close();
  if (fixture && fixture.exitCode === null) {
    try { process.kill(-fixture.pid, "SIGTERM"); } catch {}
    await new Promise(resolve => fixture.once("exit", resolve));
  }
});

test("update waits for service restart before reloading", async () => {
  const context = await browser.newContext({ viewport: { width: 1440, height: 1000 } });
  const page = await context.newPage();
  let installStarted = false;
  let healthChecksAfterInstall = 0;
  await page.route("**/admin/api/update**", async route => {
    if (route.request().method() === "POST") {
      installStarted = true;
      await route.fulfill({ status: 202, contentType: "application/json", body: JSON.stringify({ message: "更新已安装，服务即将重启" }) });
      return;
    }
    await route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ currentVersion: "test", latestVersion: "next", updateAvailable: true, canInstall: true, sources: [] }) });
  });
  await page.route("**/healthz", async route => {
    if (!installStarted) {
      await route.fulfill({ status: 200, body: "ok" });
      return;
    }
    healthChecksAfterInstall += 1;
    await route.fulfill({ status: healthChecksAfterInstall === 1 ? 503 : 200, body: healthChecksAfterInstall === 1 ? "restarting" : "ok" });
  });
  await page.goto(base + "/admin/");
  await page.locator("#admin-token").fill("test-admin-123");
  await page.locator("#login-submit").click();
  await page.locator("#dashboard").waitFor({ state: "visible" });
  await selectView(page, "settings-tab");
  await page.locator("#settings-group-update").click();
  await page.locator("#update-install").waitFor({ state: "visible" });
  const startedAt = Date.now();
  const navigation = page.waitForEvent("framenavigated");
  await page.locator("#update-install").click();
  await navigation;
  assert.ok(Date.now() - startedAt < 5000, "reload should follow service recovery, not a fixed five-second delay");
  assert.ok(healthChecksAfterInstall >= 2);
  await page.locator("#dashboard").waitFor({ state: "visible" });
  await context.close();
});

test("key lifecycle, policy, audit, conflict and responsive UI", async () => {
  const context = await browser.newContext({ viewport: { width: 1440, height: 1000 } });
  const page = await context.newPage();
  page.on("pageerror", error => failures.push(error.message));
  await page.route("**/admin/api/update**", route => route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ currentVersion: "test", sources: [] }) }));
  await page.goto(base + "/admin/");
  await page.locator("#admin-token").fill("test-admin-123");
  await page.locator("#login-submit").click();
  await page.locator("#dashboard").waitFor({ state: "visible" });
  await selectView(page, "keys-tab");
  await page.locator("#keys-empty").waitFor({ state: "visible" });
  await page.locator("#keys-create").click();
  await page.locator("#key-name").fill("Browser client <script>");
  await page.locator("#key-rpm").fill("1");
  await page.locator("#key-ip").fill("127.0.0.1");
  await page.locator("#key-models option[value='allowed-model']").waitFor({ state: "attached" });
  await page.locator("#key-models").selectOption(["allowed-model"]);
  await page.locator("#key-save").click();
  await page.locator("#key-secret-dialog").waitFor({ state: "visible" });
  const secret = await page.locator("#key-secret").inputValue();
  assert.match(secret, /^sk-[a-f0-9]{32}$/);
  await page.locator("[data-close='key-secret-dialog']").last().click();
  assert.equal(await page.locator("#key-secret").inputValue(), "");
  await page.waitForFunction(() => document.querySelectorAll("#keys-rows tr").length === 1);
  const list = await (await context.request.get(base + "/admin/api/keys")).json();
  const id = list.keys[0].id;
  assert.equal(JSON.stringify(list).includes(secret), false);
  const infer = async (model, token = secret) => {
    const response = await context.request.post(base + "/v1/chat/completions", { headers: { Authorization: "Bearer " + token }, data: { model, messages: [{ role: "user", content: "hello" }] } });
    return { status: response.status(), body: await response.json(), headers: response.headers() };
  };
  assert.equal((await infer("allowed-model")).status, 200);
  assert.equal((await infer("other-model")).status, 403);
  const limited = await infer("allowed-model");
  assert.equal(limited.status, 429); assert.ok(limited.headers["retry-after"]);
  await page.locator("#keys-rows").getByRole("button", { name: "停用", exact: true }).click();
  await page.locator("#keys-rows").getByRole("button", { name: "启用", exact: true }).waitFor();
  assert.equal((await infer("allowed-model")).status, 401);
  await page.locator("#keys-rows").getByRole("button", { name: "编辑", exact: true }).click();
  await page.locator("#key-rpm").fill("");
  await page.locator("#key-enabled").check();
  await page.locator("#key-expiry").fill("");
  await page.locator("#key-models").selectOption([]);
  await page.locator("#key-save").click();
  await page.locator("#key-editor").waitFor({ state: "hidden" });
  assert.equal((await infer("other-model")).status, 200);
  const current = (await (await context.request.get(base + "/admin/api/keys")).json()).keys[0];
  assert.equal(current.rpm, undefined); assert.equal(current.expiresAt, undefined);
  await context.request.put(base + "/admin/api/keys/" + id, { data: { version: current.version, name: "Updated elsewhere" } });
  await page.locator("#keys-rows").getByRole("button", { name: "编辑", exact: true }).click();
  await page.locator("#key-name").fill("Stale edit");
  await page.locator("#key-save").click();
  await page.locator("#keys-error").waitFor({ state: "visible" });
  assert.match(await page.locator("#keys-rows").textContent(), /Updated elsewhere/);
  await page.locator("#keys-rows").getByRole("button", { name: "轮换", exact: true }).click();
  await page.locator("#key-confirm-submit").click();
  await page.locator("#key-secret-dialog").waitFor({ state: "visible" });
  const rotated = await page.locator("#key-secret").inputValue();
  assert.notEqual(rotated, secret);
  assert.equal((await infer("allowed-model")).status, 401);
  assert.equal((await infer("allowed-model", rotated)).status, 200);
  await page.locator("[data-close='key-secret-dialog']").last().click();
  await page.locator("#keys-rows").getByRole("button", { name: "记录", exact: true }).click();
  await page.waitForFunction(() => document.querySelectorAll("#request-record-rows tr").length >= 5);
  assert.equal(await page.locator("#request-filter-key").inputValue(), id);
  const audit = await (await context.request.get(base + "/admin/api/request-records?key=" + id)).json();
  assert.ok(audit.records.every(record => record.apiKeyId === id && record.clientIP && record.requestId));
  assert.ok(audit.records.some(record => record.authErrorCode === "rpm_exceeded"));
  assert.equal(JSON.stringify(audit).includes(secret), false);
  await selectView(page, "keys-tab");
  await page.waitForFunction(() => !document.querySelector("#keys-refresh").disabled);
  mkdirSync("test-results", { recursive: true });
  for (const width of [1440, 768, 390, 320]) {
    await page.setViewportSize({ width, height: 1000 });
    await page.screenshot({ path: "test-results/keys-" + width + ".png", fullPage: true });
    const overflow = await page.evaluate(() => document.documentElement.scrollWidth > innerWidth);
    assert.equal(overflow, false, "page overflow at " + width);
    assert.ok(await page.locator(width <= 900 ? "#keys-cards" : "#keys-table-wrap").isVisible());
  }
  await page.setViewportSize({ width: 1440, height: 1000 });
  await page.locator("#keys-rows").getByRole("button", { name: "删除", exact: true }).click();
  await page.locator("#key-confirm-submit").click();
  await page.locator("#keys-empty").waitFor({ state: "visible" });
  assert.equal((await infer("allowed-model", rotated)).status, 401);
  await page.reload();
  assert.equal(await page.locator("#key-secret").inputValue(), "");
  assert.deepEqual(failures, []);
  await context.close();
});

test("key expiry, IP, clipboard failure, logout and late secret response", async () => {
  const context = await browser.newContext({ viewport: { width: 390, height: 844 } });
  await context.addInitScript(() => {
    Object.defineProperty(navigator, "clipboard", { value: { writeText: async () => { throw new Error("denied"); } } });
    document.execCommand = () => false;
  });
  const page = await context.newPage();
  await page.route("**/admin/api/update**", route => route.fulfill({ contentType:"application/json",body:JSON.stringify({currentVersion:"test",sources:[]}) }));
  await page.goto(base + "/admin/"); await page.locator("#admin-token").fill("test-admin-123"); await page.locator("#login-submit").click(); await page.locator("#dashboard").waitFor({state:"visible"});
  const create = async data => { const response = await context.request.post(base+"/admin/api/keys",{data}); assert.equal(response.status(),201); return response.json(); };
  const expired = await create({name:"Expired browser",expiresAt:"2000-01-01T00:00:00Z"});
  const ip = await create({name:"IP browser",ipAllow:["192.0.2.5"]});
  for (const [item,status] of [[expired,401],[ip,403]]) assert.equal((await context.request.get(base+"/v1/models",{headers:{Authorization:"Bearer "+item.secret}})).status(),status);
  await selectView(page, "keys-tab"); await page.locator("#keys-cards").getByText("Expired browser",{exact:true}).waitFor();
  assert.ok((await page.locator("#keys-cards").textContent()).includes("已过期"));
  await page.locator("#keys-create").click(); await page.locator("#key-name").fill("Clipboard browser"); await page.locator("#key-save").click(); await page.locator("#key-secret-dialog").waitFor({state:"visible"});
  await page.locator("#key-copy").click(); await page.locator("#key-copy-error").waitFor({state:"visible"});
  await page.evaluate(() => document.querySelector("#logout-button").click()); await page.locator("#login-panel").waitFor({state:"visible"});
  assert.equal(await page.locator("#key-secret").inputValue(),""); assert.equal(await page.locator("#key-secret-dialog").isVisible(),false);
  await page.locator("#admin-token").fill("test-admin-123"); await page.locator("#login-submit").click(); await page.locator("#dashboard").waitFor({state:"visible"}); await selectView(page, "keys-tab");
  let release, intercepted;
  const arrived = new Promise(resolve => { intercepted = resolve; }); const held = new Promise(resolve => { release = resolve; });
  await page.route("**/admin/api/keys",async route => { if(route.request().method() !== "POST") return route.continue(); intercepted(); await held; await route.fulfill({status:201,contentType:"application/json",body:JSON.stringify({secret:"sk-delayed-secret",key:{id:"delayed"}})}); });
  await page.locator("#keys-create").click(); await page.locator("#key-name").fill("Delayed"); await page.locator("#key-save").click(); await arrived;
  await page.evaluate(() => document.querySelector("#logout-button").click()); await page.locator("#login-panel").waitFor({state:"visible"}); release();
  await page.waitForLoadState("networkidle"); assert.equal(await page.locator("#key-secret").inputValue(),""); assert.equal(await page.locator("#key-secret-dialog").isVisible(),false);
  await context.request.post(base+"/admin/api/session",{data:{token:"test-admin-123"}});
  const remaining = (await (await context.request.get(base+"/admin/api/keys")).json()).keys;
  for (const item of remaining) await context.request.delete(base+"/admin/api/keys/"+item.id+"?version="+item.version);
  await context.close();
});

test("verified metering quota, unknown reservation and reconciliation UI", async () => {
  const context = await browser.newContext({viewport:{width:1440,height:1000}}); const page = await context.newPage();
  await page.route("**/admin/api/update**",route=>route.fulfill({contentType:"application/json",body:JSON.stringify({currentVersion:"test",sources:[]})}));
  await page.goto(base+"/admin/"); await page.locator("#admin-token").fill("test-admin-123"); await page.locator("#login-submit").click(); await page.locator("#dashboard").waitFor({state:"visible"});
  const policies = await (await context.request.get(base+"/admin/api/model-policies")).json(); const rule = policies.models.find(item=>item.sourceId==="allowed-model");
  const changed = await context.request.put(base+"/admin/api/model-policies/allowed-model",{data:{revision:policies.revision,rule:{...rule,metering:{source:"upstream-usage-v1",tokenUpperBound:80,creditUpperBound:10}}}}); assert.equal(changed.status(),200);
  await selectView(page, "keys-tab"); await page.locator("#keys-create").click(); await page.locator("#key-name").fill("Meter browser"); await page.locator("#key-dailyTokens").fill("100"); await page.locator("#key-dailyCredits").fill("20"); await page.locator("#key-save").click(); await page.locator("#key-secret-dialog").waitFor({state:"visible"});
  const secret = await page.locator("#key-secret").inputValue(); await page.locator("[data-close='key-secret-dialog']").last().click();
  const infer = content=>context.request.post(base+"/v1/chat/completions",{headers:{Authorization:"Bearer "+secret},data:{model:"allowed-model",messages:[{role:"user",content}]}});
  assert.equal((await infer("fixture:metered")).status(),200); assert.equal((await infer("unknown" )).status(),200); assert.equal((await infer("fixture:metered")).status(),429);
  await selectView(page, "settings-tab"); await page.locator("#settings-group-admission").click(); await page.getByRole("button",{name:"确认对账",exact:true}).waitFor();
  const pending = page.locator(".metering-pending form").filter({has:page.getByRole("button",{name:"确认对账",exact:true})}).last();
  for (const width of [1440, 768, 390, 320]) {
    await page.setViewportSize({width, height:844}); await pending.scrollIntoViewIfNeeded();
    await page.screenshot({path:"test-results/metering-pending-"+width+".png"});
    assert.equal(await page.evaluate(()=>document.documentElement.scrollWidth>innerWidth), false);
  }
  await pending.locator("input").nth(0).fill("10"); await pending.locator("input").nth(1).fill("2"); await pending.getByRole("button",{name:"确认对账",exact:true}).click(); await page.getByRole("button",{name:"确认对账",exact:true}).waitFor({state:"hidden"});
  const view = await (await context.request.get(base+"/admin/api/admission")).json(); assert.ok(Object.values(view.reservations).every(item=>item.state==="settled"));
  await page.setViewportSize({width:1440,height:1000}); await selectView(page, "keys-tab"); await page.locator("#keys-refresh").click();
  await page.waitForFunction(()=>document.querySelector("#keys-rows").textContent.includes("Meter browser"));
  const meterRow=page.locator("#keys-rows tr").filter({hasText:"Meter browser"});
  await meterRow.getByRole("button",{name:"编辑",exact:true}).click();
  for(const width of [1440,768,390,320]) {
    await page.setViewportSize({width,height:844}); await page.locator("#key-dailyTokens").scrollIntoViewIfNeeded();
    await page.screenshot({path:"test-results/metering-key-"+width+".png"});
    assert.equal(await page.locator("#key-dailyTokens").inputValue(), "100");
    await page.locator("#key-save").scrollIntoViewIfNeeded();
    assert.equal(await page.locator("#key-save").isVisible(), true);
    assert.equal(await page.evaluate(()=>document.documentElement.scrollWidth>innerWidth),false);
  }
  await page.locator("#key-editor [data-close]").first().click();
  const key=(await(await context.request.get(base+"/admin/api/keys")).json()).keys.find(item=>item.name==="Meter browser"); await context.request.delete(base+"/admin/api/keys/"+key.id+"?version="+key.version);
  const latest=await(await context.request.get(base+"/admin/api/model-policies")).json(); await context.request.delete(base+"/admin/api/model-policies/allowed-model",{data:{revision:latest.revision}});
  await context.close();
});

test("model policies edit, alias filtering, disable and conflict", async () => {
  const context = await browser.newContext({ viewport: { width: 1440, height: 1000 } });
  const page = await context.newPage();
  page.on("pageerror", error => failures.push(error.message));
  await page.route("**/admin/api/update**", route => route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ currentVersion: "test", sources: [] }) }));
  await page.goto(base + "/admin/");
  await page.locator("#admin-token").fill("test-admin-123");
  await page.locator("#login-submit").click();
  await page.locator("#dashboard").waitFor({ state: "visible" });
  await selectView(page, "models-tab");
  await page.locator("#model-rows").getByRole("button", { name: "编辑策略" }).first().waitFor();
  const created = await (await context.request.post(base + "/admin/api/keys", { data: { name: "model-policy-browser", models: ["allowed-model"] } })).json();
  const row = page.locator("#model-rows tr").filter({ hasText: "allowed-model" });
  await row.getByRole("button", { name: "编辑策略" }).click();
  await page.locator("#model-policy-public").fill("public-model");
  await page.locator("#model-policy-endpoints").selectOption("known");
  await page.locator("input[name=model-endpoint][value='/v1/chat/completions']").check();
  await page.locator("#model-policy-stream").selectOption("false");
  await page.locator("#model-policy-save").click();
  await page.locator("#model-policy-editor").waitFor({ state: "hidden" });
  assert.match(await row.textContent(), /public-model/);
  const headers = { Authorization: "Bearer " + created.secret };
  const models = await (await context.request.get(base + "/v1/models", { headers })).json();
  assert.deepEqual(models.data.map(item => item.id), ["public-model"]);
  let response = await context.request.post(base + "/v1/chat/completions", { headers, data: { model: "public-model", stream: true } });
  assert.equal(response.status(), 403);
  response = await context.request.post(base + "/v1/chat/completions", { headers, data: { model: "public-model" } });
  assert.equal(response.status(), 200);
  assert.equal((await context.request.post(base + "/v1/responses", { headers, data: { model: "public-model", input: "fixture" } })).status(), 403);
  await row.getByRole("button", { name: "编辑策略" }).click();
  for (const input of await page.locator("input[name=model-endpoint]").all()) await input.check();
  await page.locator("#model-policy-save").click(); await page.locator("#model-policy-editor").waitFor({ state: "hidden" });
  const responses = await context.request.post(base + "/v1/responses", { headers, data: { model: "public-model", input: "fixture" } });
  assert.equal(responses.status(), 200); assert.equal((await responses.json()).model, "public-model");
  await row.getByRole("button", { name: "测试", exact: true }).click();
  assert.equal(await page.locator("#playground-model").inputValue(), "public-model");
  await page.locator("#playground-dialog button[aria-label=关闭]").click();
  await row.getByRole("button", { name: "编辑策略" }).click();
  await page.locator("#model-policy-enabled").uncheck();
  await page.locator("#model-policy-save").click();
  await page.locator("#model-policy-editor").waitFor({ state: "hidden" });
  assert.equal((await context.request.post(base + "/v1/chat/completions", { headers, data: { model: "public-model" } })).status(), 403);
  assert.deepEqual((await (await context.request.get(base + "/v1/models", { headers })).json()).data, []);
  await row.getByRole("button", { name: "编辑策略" }).click();
  const snapshot = await (await context.request.get(base + "/admin/api/model-policies")).json();
  const rule = snapshot.models.find(item => item.sourceId === "allowed-model");
  await context.request.put(base + "/admin/api/model-policies/allowed-model", { data: { revision: snapshot.revision, rule: { ...rule, publicId: "changed-elsewhere" } } });
  await page.locator("#model-policy-public").fill("stale-name");
  await page.locator("#model-policy-save").click();
  await page.waitForFunction(() => document.querySelector("#model-policy-error").textContent.includes("刷新"));
  assert.match(await row.textContent(), /changed-elsewhere/);
  await page.locator("[data-close=model-policy-editor]").click();
  for (const width of [1440, 768, 390, 320]) {
    await page.setViewportSize({ width, height: 1000 });
    await page.screenshot({ path: "test-results/models-" + width + ".png", fullPage: true });
    assert.equal(await page.evaluate(() => document.documentElement.scrollWidth > innerWidth), false, "model overflow at " + width);
  }
  await page.setViewportSize({ width: 1440, height: 1000 });
  await row.getByRole("button", { name: "编辑策略" }).click();
  await page.locator("#model-policy-reset").click();
  await page.locator("#model-policy-editor").waitFor({ state: "hidden" });
  assert.equal((await context.request.post(base + "/v1/chat/completions", { headers, data: { model: "allowed-model" } })).status(), 200);
  await context.request.delete(base + "/admin/api/keys/" + created.key.id + "?version=" + created.key.version);
  const cooldownKey = await (await context.request.post(base + "/admin/api/keys", { data: { name: "cooldown-browser" } })).json();
  const cooldownHeaders = { Authorization: "Bearer " + cooldownKey.secret };
  const limited = await context.request.post(base + "/v1/chat/completions", { headers: cooldownHeaders, data: { model: "other-model", messages: [{ role: "user", content: "fixture:429" }] } });
  assert.equal(limited.status(), 429);
  const cooldowns = await (await context.request.get(base + "/admin/api/model-cooldowns")).json();
  assert.ok(cooldowns.entries.some(item => item.accountId === "fixture" && item.model === "other-model" && item.reason === "upstream_429"));
  assert.equal((await context.request.post(base + "/v1/chat/completions", { headers: cooldownHeaders, data: { model: "allowed-model" } })).status(), 200);
  await selectView(page, "accounts-tab");
  await page.waitForFunction(() => document.querySelector("#account-rows").textContent.includes("other-model"));
  assert.match(await page.locator("#account-rows").textContent(), /上游限流.*恢复/);
  await context.request.delete(base + "/admin/api/keys/" + cooldownKey.key.id + "?version=" + cooldownKey.key.version);
  assert.deepEqual(failures, []);
  await context.close();
});
