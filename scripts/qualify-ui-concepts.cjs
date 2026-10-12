"use strict";
// Standalone, disposable browser qualification. Serves only mockup assets on loopback.
const { chromium } = require(
  process.env.PLAYWRIGHT_MODULE_PATH || "playwright",
);
const http = require("node:http");
const fs = require("node:fs");
const path = require("node:path");
const assert = require("node:assert/strict");
const assets = path.resolve(__dirname, "../docs/ui-concepts");
const server = http.createServer((req, res) => {
  const file = {
    "/": "index.html",
    "/index.html": "index.html",
    "/concepts.js": "concepts.js",
    "/concepts.css": "concepts.css",
  }[new URL(req.url, "http://localhost").pathname];
  if (!file) {
    res.writeHead(404);
    res.end();
    return;
  }
  res.setHeader(
    "Content-Type",
    file.endsWith(".js")
      ? "text/javascript"
      : file.endsWith(".css")
        ? "text/css"
        : "text/html",
  );
  res.end(fs.readFileSync(path.join(assets, file)));
});
(async () => {
  await new Promise((resolve) => server.listen(0, "127.0.0.1", resolve));
  const origin = `http://127.0.0.1:${server.address().port}`;
  let browser;
  try {
    browser = await chromium.launch({
      executablePath: process.env.CHROMIUM_PATH || "/usr/bin/chromium",
      headless: true,
    });
    const page = await browser.newPage({
      viewport: { width: 1440, height: 1050 },
    });
    const errors = [],
      unexpected = [];
    page.on("pageerror", (e) => errors.push(e.message));
    page.on("request", (req) => {
      if (!req.url().startsWith(origin + "/")) unexpected.push(req.url());
    });
    const names = [
      "Guided",
      "Overview",
      "Access studio",
      "Workbench",
      "Launcher",
    ];
    for (let i = 0; i < names.length; i++) {
      await page.goto(origin);
      await page.locator(`[data-design="${i}"]`).click();
      assert.equal(await page.locator("#app h1").count(), 1);
      if (process.env.APC_UI_SCREENSHOT_DIR) {
        fs.mkdirSync(process.env.APC_UI_SCREENSHOT_DIR, { recursive: true });
        await page.screenshot({
          path: path.join(
            process.env.APC_UI_SCREENSHOT_DIR,
            `concept-${i + 1}.png`,
          ),
          fullPage: true,
        });
      }
      await page
        .getByLabel("Repository", { exact: true })
        .selectOption("demo/data-pipeline");
      assert.equal(
        await page.getByLabel("Sandbox name", { exact: true }).inputValue(),
        "data-pipeline",
      );
      await page
        .getByLabel("Sandbox name", { exact: true })
        .fill(`custom-${i}`);
      await page
        .getByLabel("Repository", { exact: true })
        .selectOption("demo/rust-worker");
      assert.equal(
        await page.getByLabel("Sandbox name", { exact: true }).inputValue(),
        `custom-${i}`,
      );
      if (i === 0)
        await page
          .getByRole("button", { name: "Continue", exact: false })
          .click();
      await page.getByLabel("Full public web", { exact: false }).check();
      if (i === 0)
        await page
          .getByRole("button", { name: "Continue", exact: false })
          .click();
      await page
        .getByRole("switch", { name: "Create a test cluster", exact: false })
        .check();
      await page.getByLabel("Cluster size").selectOption("3");
      if (i === 0)
        await page
          .getByRole("button", { name: "Continue", exact: false })
          .click();
      await page
        .getByRole("button", { name: "Create demo sandbox", exact: false })
        .click();
      const project = page
        .locator(".project")
        .filter({
          has: page.getByRole("heading", { name: `custom-${i}`, exact: true }),
        });
      await project.waitFor();
      assert.match(await project.textContent(), /Public web/);
      assert.match(await project.textContent(), /3-node test cluster/);
      await project.getByRole("button", { name: "Pause", exact: true }).click();
      await project
        .getByRole("button", { name: "Resume", exact: true })
        .click();
      await project
        .getByRole("button", { name: "Access & cluster", exact: true })
        .click();
      await page
        .locator("#editor")
        .getByLabel("Selected destinations", { exact: false })
        .check();
      await page
        .locator("#editor")
        .getByRole("switch", { name: "Create a test cluster", exact: false })
        .uncheck();
      await page
        .getByRole("button", { name: "Save demo changes", exact: false })
        .click();
      assert.match(await project.textContent(), /Selected access/);
      assert.match(await project.textContent(), /No test cluster/);
      await page.setViewportSize({ width: 390, height: 844 });
      assert.equal(
        await page.evaluate(
          () => document.documentElement.scrollWidth <= innerWidth,
        ),
        true,
        `${names[i]} overflows on mobile`,
      );
      if (process.env.APC_UI_SCREENSHOT_DIR)
        await page.screenshot({
          path: path.join(
            process.env.APC_UI_SCREENSHOT_DIR,
            `concept-${i + 1}-mobile.png`,
          ),
          fullPage: true,
        });
      await page.setViewportSize({ width: 1440, height: 1050 });
    }
    await page.goto(origin + "/#studio");
    await page.locator("#probe").click();
    assert.match(await page.locator("#probe-result").textContent(), /^Allowed/);
    for (const url of [
      "https://localhost/",
      "https://127.0.0.1/",
      "https://10.0.0.1/",
      "https://169.254.169.254/",
      "https://[::1]/",
      "https://service.internal/",
      "http://api.example.com/v1/items/42",
    ]) {
      await page.locator("#probe-url").fill(url);
      await page.locator("#probe").click();
      assert.match(
        await page.locator("#probe-result").textContent(),
        /^Blocked/,
      );
    }
    await page.locator('[data-key="match"]').selectOption("exact");
    await page.locator('[data-key="path"]').fill("/v1/items/a.b");
    await page
      .locator("#probe-url")
      .fill("https://api.example.com/v1/items/axb");
    await page.locator("#probe").click();
    assert.match(await page.locator("#probe-result").textContent(), /^Blocked/);
    await page
      .locator("#probe-url")
      .fill("https://api.example.com/v1/items/a.b");
    await page.locator("#probe").click();
    assert.match(await page.locator("#probe-result").textContent(), /^Allowed/);
    await page.locator("#probe-method").selectOption("POST");
    await page.locator("#probe").click();
    assert.match(await page.locator("#probe-result").textContent(), /^Blocked/);
    await page.locator('[data-key="intent"]').selectOption("submit");
    await page.locator("#probe").click();
    assert.match(await page.locator("#probe-result").textContent(), /^Allowed/);
    await page.locator('[data-key="host"]').fill("localhost");
    await page
      .getByRole("button", { name: "Create demo sandbox", exact: false })
      .click();
    assert.match(await page.locator("#toast").textContent(), /public hostname/);
    await page.getByLabel("Full public web", { exact: false }).check();
    await page.locator("#probe-url").fill("https://192.168.1.1/");
    await page.locator("#probe").click();
    assert.match(await page.locator("#probe-result").textContent(), /^Blocked/);
    await page.getByLabel("No outbound access", { exact: false }).check();
    await page
      .locator("#probe-url")
      .fill("https://api.example.com/v1/items/42");
    await page.locator("#probe").click();
    assert.match(await page.locator("#probe-result").textContent(), /^Blocked/);
    await page.getByRole("button", { name: "Reset demo", exact: true }).click();
    assert.equal(await page.locator(".project").count(), 2);
    assert.deepEqual(errors, []);
    assert.deepEqual(unexpected, []);
    console.log(
      "PASS: five creation/edit/lifecycle flows, name overrides, clusters, mobile layouts, literal regex matching, method denial, local-address denial, offline denial, and zero external requests.",
    );
  } finally {
    if (browser) await browser.close();
    await new Promise((resolve) => server.close(resolve));
  }
})().catch((e) => {
  console.error(e);
  process.exitCode = 1;
});
