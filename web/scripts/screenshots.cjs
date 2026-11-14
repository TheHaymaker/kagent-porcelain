/*
 * Drives the built app in mock mode and saves screenshots.
 *   npm run build && npx vite preview --port 4173 &
 *   node scripts/screenshots.cjs [baseUrl] [outDir]
 * Uses the Playwright already on the machine (require('playwright')).
 */
const path = require("node:path");
const fs = require("node:fs");
let playwright;
try {
  playwright = require("playwright");
} catch {
  playwright = require(path.join(require("node:child_process").execSync("npm root -g").toString().trim(), "playwright"));
}

const base = process.argv[2] || "http://localhost:4173";
const out = process.argv[3] || path.join(__dirname, "..", "docs", "screenshots");
fs.mkdirSync(out, { recursive: true });

const VIEWPORTS = {
  desktop: { width: 1440, height: 900 },
  phone: { width: 390, height: 844 },
};

async function shoot(page, name, full = false) {
  await page.waitForTimeout(400);
  await page.screenshot({ path: path.join(out, `${name}.png`), fullPage: full });
  console.log("saved", name);
}

/** In-app navigation: mock state lives in memory, so a full page load would reset it. */
async function nav(page, kind, label) {
  if (kind === "phone") {
    await page.getByRole("button", { name: "Open menu" }).click();
    await page.locator(".mobile-nav .nav-link", { hasText: label }).click();
  } else {
    await page.locator(".sidebar .nav-link", { hasText: label }).click();
  }
}

async function run(kind) {
  const browser = await playwright.chromium.launch();
  const context = await browser.newContext({ viewport: VIEWPORTS[kind], deviceScaleFactor: 1 });
  const page = await context.newPage();
  const errors = [];
  page.on("pageerror", (e) => errors.push(String(e)));
  page.on("console", (m) => m.type() === "error" && errors.push(m.text()));

  await page.goto(base + "/");
  await page.getByRole("heading", { name: "What do you want to build?" }).waitFor();
  await page.getByText("Recent conversations").waitFor();
  await page.waitForSelector(".list-row");
  await shoot(page, `${kind}-home`);

  // The scripted build: a question, tools, surfaces, two approvals.
  await page.getByLabel("Describe the app you want").fill("An expense tracker where my team can upload receipts and see totals per month");
  await page.getByRole("button", { name: "Start building" }).click();
  await page.getByText("Who should be able to use it?").waitFor({ timeout: 20000 });
  await page.getByLabel("The whole finance department").check();
  await page.getByRole("button", { name: "Send answer" }).click();
  await page.getByRole("button", { name: "Approve" }).waitFor({ timeout: 30000 });
  await shoot(page, `${kind}-conversation-ordinary-approval`);
  if (process.env.DEBUG) await page.locator(".timeline-scroll").evaluate((el) => { window.__log = []; el.addEventListener("scroll", () => window.__log.push([Math.round(el.scrollTop), el.scrollHeight, el.clientHeight])); });
  await page.getByRole("button", { name: "Approve" }).click();
  await page.getByText("Waiting on Platform approvers", { exact: false }).first().waitFor({ timeout: 30000 });
  await page.waitForTimeout(800);
  if (process.env.DEBUG) console.log(await page.locator(".timeline-scroll").evaluate((el) => [el.scrollTop, el.scrollHeight, el.clientHeight]), JSON.stringify(await page.evaluate(() => window.__log)));
  await shoot(page, `${kind}-conversation`);
  if (kind === "desktop") {
    await page.locator(".timeline-scroll").evaluate((el) => (el.scrollTop = el.scrollHeight * 0.35));
    await shoot(page, `${kind}-conversation-surfaces`);
  } else {
    await page.locator(".timeline-scroll").evaluate((el) => (el.scrollTop = el.scrollHeight * 0.45));
    await shoot(page, `${kind}-conversation-surfaces`);
    await page.locator(".convo-actions button", { hasText: "Activity" }).click();
    await shoot(page, `${kind}-activity-drawer`);
    await page.keyboard.press("Escape");
  }

  await nav(page, kind, "Approvals");
  await page.waitForSelector(".inbox-row");
  if (kind === "desktop") await page.locator(".inbox-row").first().click();
  await shoot(page, `${kind}-approvals`);
  if (kind === "phone") {
    await page.locator(".inbox-row").first().click();
    await shoot(page, `${kind}-approval-detail`);
  }

  await nav(page, kind, "Apps");
  await page.waitForSelector(".app-tile");
  await shoot(page, `${kind}-apps`);
  await page.getByRole("link", { name: "invoice-reminders" }).click();
  await page.getByRole("button", { name: "Request production" }).click();
  await page.getByRole("button", { name: "Send request" }).click();
  await page.getByText("Waiting on Platform approvers", { exact: false }).first().waitFor();
  await page.locator(".toast").waitFor({ state: "detached", timeout: 8000 });
  await shoot(page, `${kind}-app-detail`);

  if (kind === "desktop") {
    for (const [label, route] of [["Usage", "usage"], ["Activity", "audit"], ["Schedules", "schedules"], ["Settings", "settings"]]) {
      await nav(page, kind, label);
      await page.waitForTimeout(700);
      await shoot(page, `${kind}-${route}`);
    }
  }
  if (errors.length) console.log("page errors:", errors);
  await browser.close();
}

(async () => {
  for (const kind of process.argv.includes("--phone-only") ? ["phone"] : process.argv.includes("--desktop-only") ? ["desktop"] : ["desktop", "phone"]) await run(kind);
})().catch((e) => {
  console.error(e);
  process.exit(1);
});
