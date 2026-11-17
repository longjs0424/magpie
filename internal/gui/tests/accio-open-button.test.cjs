const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const { agentRow } = require("./reader.cjs");
const assets = process.env.MAGPIE_ASSETS || path.resolve(__dirname, "../assets");
const words = { en: "Open", zh: "打开", "zh-TW": "開啟", ja: "開く", de: "Öffnen" };

for (const engine of process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"]) {
  for (const [lang, open] of Object.entries(words)) for (const width of [900, 400]) {
    test(`${engine} ${lang} ${width}: Accio opens directly, handles errors, and web copies only`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ executablePath: process.env.MAGPIE_TEST_BROWSER || undefined }));
      t.after(() => browser.close());
      const page = await browser.newPage({ viewport: { width, height: 800 }, reducedMotion: "reduce" });
      page.setDefaultTimeout(8000);
      let web = false, fail = false;
      const posts = [], errors = [];
      page.on("pageerror", e => errors.push(e.message));
      await page.route("**/*", async route => {
        const req = route.request(), url = new URL(req.url());
        if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs={lang:${JSON.stringify(lang)},theme:"light",web:${web}};` });
        if (url.pathname === "/api/state") return route.fulfill({ json: { agents: [{ ...agentRow("accio", "Accio Work"), openApp: true, launch: "ADK_MODEL=relay/m1 Accio" }], profiles: [], settings: { lang, theme: "light" } } });
        if (url.pathname === "/api/agents/open") {
          posts.push(req.postDataJSON());
          return route.fulfill(fail ? { status: 400, json: { error: "Accio Work is already running; fully quit it, then click Open again" } } : { status: 204, body: "" });
        }
        if (url.pathname === "/api/settings") return route.fulfill({ json: { lang, theme: "light" } });
        if (url.pathname === "/api/plugins") return route.fulfill({ json: { plugins: [] } });
        if (url.pathname.startsWith("/api/")) return route.fulfill({ json: {} });
        if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window={};" });
        const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
        try { return route.fulfill({ body: await fs.readFile(file), contentType: { ".js": "text/javascript", ".html": "text/html", ".css": "text/css", ".svg": "image/svg+xml" }[path.extname(file)] }); }
        catch { return route.fulfill({ status: 404, body: "" }); }
      });
      await page.goto("http://magpie.test/?view=agents");
      const button = page.locator('.row.agent[data-id="accio"] .launch');
      await button.waitFor({ state: "visible" });
      assert.equal(await button.textContent(), open);
      assert(await button.evaluate(e => e.scrollWidth <= e.clientWidth), "Open label overflows its button");
      const response = page.waitForResponse(r => new URL(r.url()).pathname === "/api/agents/open");
      await button.click(); await response;
      await page.waitForFunction(() => !document.querySelector('.row.agent[data-id="accio"] .launch').disabled);
      assert.deepEqual(posts, [{ agent: "accio" }]);
      fail = true;
      await button.click();
      await page.locator('#status[role="alert"]').waitFor();
      assert.equal(await button.isEnabled(), true);
      assert.equal(posts.length, 2);
      assert((await page.locator("#status").textContent()).length > 10);
      await page.screenshot({ path: path.resolve("dist/verification", `accio-open-${engine}-${lang}-${width}.png`) });
      web = true;
      await page.reload();
      await button.waitFor({ state: "visible" });
      assert.equal(await button.textContent(), "");
      assert.equal(posts.length, 2);
      assert.deepEqual(errors, []);
    });
  }
}
