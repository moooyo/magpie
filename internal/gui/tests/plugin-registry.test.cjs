// Run with Node's test runner and Playwright on the module path; see README.md.
// Settings and Plugins edit the same explicit npm registry. Automatic leaves
// Plugins' own registry discovery in place. The API is faked.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const zed = { package: "@magpie-community/opencode-zed-auth", name: "Zed", icon: "zed", providers: ["zed"], community: true,
  summary: { en: "Your Zed plan.", zh: "Zed 订阅。" } };
const NPMRC = { url: "https://npm.corp.example/repo", from: "npmrc", path: "~/.npmrc" };
const CUSTOM = "https://registry.example/repository/npm";
const LEGACY = "http://127.0.0.1:8765/npm";
const BAD = "ftp://npm.example";
const registryError = "Enter an HTTP or HTTPS npm registry URL without credentials, a query, or a fragment.";

function server(lang, asked, switchWhileChecking = false) {
  let explicit = "";
  let settings = { lang, theme: "light", tray: "panel", currency: "usd", proxy: "direct", version: "test", dir: "/test" };
  const registry = () => explicit ? { url: explicit, from: "magpie" } : NPMRC;
  const installed = { spec: zed.package, name: "Zed", version: "0.1.0", off: false, named: true, providers: [] };
  const state = () => ({ bun: true, bunVersion: "1.3.0", plugins: switchWhileChecking ? [installed] : [], mirror: true, registry: registry() });
  const metadata = () => explicit ? { version: explicit === CUSTOM ? "2.0.0" : "3.0.0", weekly: 0 } : { version: "0.1.0", weekly: 120 };
  const listings = () => [{ ...zed, ...(!explicit && { npm: metadata() }) }];
  const current = () => ({ ...settings, ...(explicit && { npmRegistry: explicit }) });
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    const json = (data, status) => route.fulfill({ json: data, status });
    if (url.host !== "magpie.test") return route.fulfill({ status: 404, body: "" });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = ${JSON.stringify({ lang, theme: "light", web: false })};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: current() });
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true }, plugins: [] });
    if (url.pathname === "/api/groups") return json({ groups: [], pools: [] });
    if (url.pathname === "/api/plugins") return json(state());
    if (url.pathname === "/api/plugins/listings") return json({ listings: listings() });
    if (url.pathname === "/api/plugins/market") return json({ listings: listings(), state: state() });
    if (url.pathname === "/api/plugins/counts") return json({ plugins: { [zed.package]: { users: 5 } }, liked: [] });
    if (url.pathname === "/api/plugins/npm") return json({ npm: { [zed.package]: metadata() } });
    if (url.pathname === "/api/plugins/check" && switchWhileChecking) {
      explicit = CUSTOM; // another window saved while the old check was running
      return json({ at: new Date().toISOString(), state: state(), plugins: [
        { ...installed, package: zed.package, latest: "8.6.0", status: "update" },
      ] });
    }
    if (url.pathname === "/api/settings" && req.method() === "GET") return json(current());
    if (url.pathname === "/api/settings" || url.pathname === "/api/plugins/registry") {
      assert.equal(req.method(), "POST");
      const data = req.postDataJSON();
      const plugin = url.pathname === "/api/plugins/registry";
      const u = plugin ? data.url : data.npmRegistry;
      asked.push({ path: url.pathname, value: u });
      if (u && !/^https?:\/\/[^/]/.test(u)) return json({ error: registryError }, 400);
      if (u !== undefined) explicit = u.replace(/\/+$/, "");
      if (plugin) return json({ registry: registry() });
      delete data.npmRegistry;
      settings = { ...settings, ...data };
      return json(current());
    }
    if (url.pathname.startsWith("/api/")) return json({});
    if (!/^\/[\w./-]*$/.test(url.pathname)) return route.fulfill({ status: 404, body: "" });
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}

const L = {
  en: { label: "Magpie-wide npm registry", from: "from ~/.npmrc", here: "Settings › Network and sharing", change: "Change", save: "Save", def: "Use default", auto: "Automatic", invalid: registryError },
  zh: { label: "Magpie 全局 npm 源", from: "来自 ~/.npmrc", here: "设置 › 网络与共享", change: "修改", save: "保存", def: "使用默认", auto: "自动", invalid: "请输入 HTTP 或 HTTPS 的 npm 源地址，不要包含登录凭据、查询参数或片段。" },
  "zh-TW": { label: "Magpie 全域 npm 來源", from: "來自 ~/.npmrc", here: "設定 › 網路與共享", change: "修改", save: "儲存", def: "使用預設", auto: "自動", invalid: "請輸入 HTTP 或 HTTPS 的 npm 來源網址，不要包含登入憑證、查詢參數或片段。" },
  ja: { label: "Magpie 全体の npm レジストリ", from: "~/.npmrc から", here: "設定 › ネットワークと共有", change: "変更", save: "保存", def: "デフォルトを使用", auto: "自動", invalid: "認証情報、クエリ、フラグメントを含まない HTTP または HTTPS の npm レジストリ URL を入力してください。" },
  de: { label: "npm-Registry für ganz Magpie", from: "aus ~/.npmrc", here: "Einstellungen › Netzwerk und Freigabe", change: "Ändern", save: "Speichern", def: "Standard verwenden", auto: "Automatisch", invalid: "Gib eine HTTP- oder HTTPS-URL für die npm-Registry ein, ohne Zugangsdaten, Abfrageparameter oder Fragment." },
};

async function fits(row) {
  const sideways = await row.evaluate((r) => ({
    page: document.documentElement.scrollWidth - document.documentElement.clientWidth,
    row: r.scrollWidth - r.clientWidth,
    right: r.getBoundingClientRect().right - r.closest("main").getBoundingClientRect().right,
  }));
  assert.ok(sideways.page <= 0 && sideways.row <= 1 && sideways.right <= 1, JSON.stringify(sideways));
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": Settings and Plugins share the npm registry", async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    for (const [lang, w] of Object.entries(L)) for (const width of [980, 380]) {
      await t.test(`${lang} ${width}px`, async (t) => {
        const context = await browser.newContext({ viewport: { width, height: 820 }, reducedMotion: "reduce" });
        t.after(() => context.close());
        const page = await context.newPage();
        page.setDefaultTimeout(8000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [], asked = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", server(lang, asked));
        await page.goto("http://magpie.test/?view=plugins");
        const row = page.locator("#view-plugins .pm-registry");
        await row.locator(".pm-reg-from", { hasText: w.from }).waitFor();
        assert.equal((await row.locator(".pm-mlabel").innerText()).trim(), w.label);
        assert.equal((await row.locator(".pm-reg-url").innerText()).trim(), NPMRC.url);
        await fits(row);
        const market = page.locator("#view-plugins");
        await market.locator(".pm-ver", { hasText: "v0.1.0" }).first().waitFor();
        assert.ok(await market.locator(".pm-card [title*='120']").count(), "automatic mode shows npm download counts");

        // Keep the same page: returning to Discover must not reuse metadata
        // that the browser kept for the previous upstream.
        await page.evaluate(async () => { await show("settings"); setSetTab("network", true); });
        await page.waitForFunction(() => typeof renderNPMRegistry === "function");
        const prefs = page.locator("#npmRegistryRow"), field = prefs.getByLabel(w.label);
        const save = prefs.getByRole("button", { name: w.save });
        assert.equal(await field.inputValue(), "");
        assert.equal(await field.getAttribute("placeholder"), w.auto);
        assert.equal(await save.isDisabled(), true);
        assert.deepEqual(asked, [], "opening Settings sends no registry request");
        await fits(prefs);

        await field.fill(BAD);
        await field.press("Enter");
        const error = page.locator("#npmRegistryError");
        await error.waitFor();
        assert.equal(await error.textContent(), w.invalid);
        assert.equal(await field.inputValue(), BAD, "the error keeps the address editable");
        await field.press("Escape");
        assert.equal(await field.inputValue(), "");
        assert.equal(await error.isHidden(), true);
        await field.fill(" " + CUSTOM + "/ ");
        await save.click();
        await page.waitForFunction((v) => document.querySelector("#npmRegistryInput").value === v, CUSTOM);
        assert.equal(await save.isDisabled(), true);
        await page.evaluate(() => show("plugins"));
        await row.locator(".pm-reg-from", { hasText: w.here }).waitFor();
        assert.equal(await row.locator(".pm-reg-url").innerText(), CUSTOM);
        await market.locator(".pm-ver", { hasText: "v2.0.0" }).first().waitFor();
        assert.equal(await market.locator(".pm-ver", { hasText: "v0.1.0" }).count(), 0);
        assert.equal(await market.locator(".pm-card [title*='120']").count(), 0);

        await row.getByRole("button", { name: w.change }).click();
        const pluginField = row.getByRole("textbox", { name: w.label });
        await pluginField.waitFor();
        assert.equal(await pluginField.inputValue(), CUSTOM);
        assert.ok(await pluginField.evaluate((i) => document.activeElement === i), "the field has the focus");
        await pluginField.fill(BAD);
        await pluginField.press("Enter");
        await row.locator(".pm-reg-err").waitFor();
        assert.equal(await row.locator(".pm-reg-err").innerText(), w.invalid);
        assert.equal(await pluginField.inputValue(), BAD);
        await pluginField.fill(LEGACY + "/");
        await row.getByRole("button", { name: w.save }).click();
        await row.locator(".pm-reg-url", { hasText: LEGACY }).waitFor();
        await row.getByRole("button", { name: w.def }).click();
        await row.locator(".pm-reg-from", { hasText: w.from }).waitFor();
        assert.equal(await row.getByRole("button", { name: w.def }).count(), 0);
        await page.evaluate(() => show("settings"));
        await page.waitForFunction(() => document.querySelector("#npmRegistryInput").value === "");
        await field.fill(CUSTOM);
        await field.press("Enter");
        await page.waitForFunction(() => document.querySelector("#npmRegistrySave").disabled && !document.querySelector("#npmRegistryInput").disabled);
        await field.fill("");
        await save.click();
        await page.waitForFunction(() => document.querySelector("#npmRegistrySave").disabled && !document.querySelector("#npmRegistryInput").disabled);
        assert.deepEqual(asked, [
          { path: "/api/settings", value: BAD },
          { path: "/api/settings", value: CUSTOM + "/" },
          { path: "/api/plugins/registry", value: BAD },
          { path: "/api/plugins/registry", value: LEGACY + "/" },
          { path: "/api/plugins/registry", value: "" },
          { path: "/api/settings", value: CUSTOM },
          { path: "/api/settings", value: "" },
        ]);
        assert.deepEqual(errors, []);
      });
    }
  });

  test(engine + ": an update check cannot put back versions from the old registry", async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    const page = await browser.newPage({ viewport: { width: 380, height: 820 }, reducedMotion: "reduce" });
    page.setDefaultTimeout(8000);
    page.setDefaultNavigationTimeout(60000);
    await page.route("**/*", server("en", [], true));
    await page.goto("http://magpie.test/?view=plugins");
    const discover = page.locator("#view-plugins"), week = discover.locator(".pm-recs");
    await week.getByText("v0.1.0", { exact: true }).waitFor();
    await discover.locator('[data-seg="installed"]').click();
    await page.locator(".pm-check").click();
    await page.getByRole("button", { name: "Check for updates" }).waitFor();
    assert.equal(await page.getByText(/8\.6\.0/).count(), 0);
    await page.getByRole("button", { name: "Discover", exact: true }).click();
    await page.locator(".pm-reg-url", { hasText: CUSTOM }).waitFor();
    await week.getByText("v2.0.0", { exact: true }).waitFor();
    assert.equal(await week.locator(".pm-rec.ghost").count(), 0);
    assert.equal(await discover.getByText(/8\.6\.0/).count(), 0);
  });
}
