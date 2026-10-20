// End-to-end browser test of the web console.
//
// Drives the real page in headless Chrome against a real server and checks:
//   1. the page loads without JavaScript errors
//   2. the one-click self-test passes
//   3. manual flow through the UI: connect, login, enter, bag, use item, GM, chat, move
//   4. two browsers see each other (AOI), receive each other's chat and movement
//   5. duplicate login kicks the first browser, which shows the reason
//   6. error toast and language switch
//
// Run with: docker compose -f test/webconsole/compose.yml up --build --abort-on-container-exit --exit-code-from browser
const puppeteer = require("puppeteer");
const fs = require("fs");

const BASE = process.env.CONSOLE_URL || "http://server:9100/console/";
const OUT = process.env.OUT_DIR || "/out";
const results = [];
const pageErrors = [];

function step(name, ok, detail = "") {
  results.push({ name, ok, detail });
  console.log(`${ok ? "PASS" : "FAIL"}  ${name}${detail ? "  — " + detail : ""}`);
  if (!ok) throw new Error(`step failed: ${name}: ${detail}`);
}

async function openConsole(browser, label) {
  const ctx = await browser.createBrowserContext(); // isolated, like a separate user
  const page = await ctx.newPage();
  await page.setViewport({ width: 1440, height: 1000 });
  page.on("pageerror", (e) => pageErrors.push(`${label}: ${e.message}`));
  page.on("console", (m) => { if (m.type() === "error") pageErrors.push(`${label} console: ${m.text()}`); });
  await page.goto(BASE, { waitUntil: "networkidle0" });
  await page.waitForFunction(() => document.body.dataset.ready === "1", { timeout: 10000 });
  return page;
}

const stateOf = (page) => page.$eval("#statusPill", (el) => el.dataset.state);
const waitState = (page, s) =>
  page.waitForFunction((s) => document.getElementById("statusPill").dataset.state === s, { timeout: 8000 }, s);
const text = (page, sel) => page.$eval(sel, (el) => el.textContent.trim());

async function loginAndEnter(page, account, name) {
  await page.click("#btnConnect");
  await waitState(page, "connected");
  await page.$eval("#account", (el, v) => { el.value = v; }, account);
  await page.$eval("#charName", (el, v) => { el.value = v; }, name);
  await page.click("#btnLogin");
  await waitState(page, "loggedin");
  await page.click("#btnEnter");
  await waitState(page, "ingame");
}

async function waitText(page, sel, predicateSrc, arg, timeout = 5000) {
  return page.waitForFunction(
    (sel, src, arg) => {
      const el = document.querySelector(sel);
      return el && new Function("t", "arg", src)(el.textContent, arg);
    },
    { timeout }, sel, predicateSrc, arg);
}

(async () => {
  fs.mkdirSync(OUT, { recursive: true });
  const browser = await puppeteer.launch({ args: ["--no-sandbox", "--disable-dev-shm-usage"] });
  const suffix = Date.now().toString(36);
  try {
    // ---------------------------------------------------------------- 1. load
    const a = await openConsole(browser, "A");
    const wsUrl = await a.$eval("#wsUrl", (el) => el.value);
    step("page loads, schema fetched, WebSocket URL prefilled", wsUrl === "ws://server:7001/ws", wsUrl);
    step("initial state is disconnected", (await stateOf(a)) === "disconnected");

    // ---------------------------------------------------------------- 2. self-test
    await a.click("#btnSelfTest");
    await a.waitForFunction(() => ["pass", "fail"].includes(document.body.dataset.selftest), { timeout: 30000 });
    const st = await a.evaluate(() => window.__selftest);
    for (const r of st.results) console.log(`      self-test ${r.status.padEnd(4)} ${r.title}  ${r.detail ?? ""}`);
    step("one-click self-test passes", st.ok, `${st.passed} passed, ${st.skipped} skipped, ${st.failed} failed in ${st.ms} ms`);
    step("self-test checked every step", st.passed >= 12, `${st.passed}/${st.total}`);
    const summary = await text(a, "#selftestSummary");
    step("self-test summary shown to the user", summary.startsWith("✔"), summary);
    await a.screenshot({ path: `${OUT}/1-selftest.png`, fullPage: true });

    // ---------------------------------------------------------------- 3. manual flow (A)
    const accA = "alice_" + suffix;
    await loginAndEnter(a, accA, "Alice");
    step("A: connect → login → enter game via buttons", true, accA);
    step("A: uid shown in header", /^\d+$/.test(await text(a, "#uidText")));
    const logText = await text(a, "#logList");
    step("A: password is masked in the message log", logText.includes('"password":"******"') && !logText.includes('"password":"123456"'));
    step("A: character card shows name and level", (await text(a, "#pName")) === "Alice" && (await text(a, "#pLevel")) === "Lv.1");
    await a.waitForFunction(() => document.querySelectorAll("#bagBody tr").length === 3, { timeout: 5000 });
    step("A: bag shows 3 starter stacks", true);

    await a.click('#bagBody button[data-item="1002"]');
    await waitText(a, "#pGold", "return t === '100'");
    step("A: click Use on Gold Pouch → gold 0 → 100 (push updated the UI)", true);
    const pouch = await a.$$eval("#bagBody tr", (rows) => rows.find((r) => r.children[0].textContent === "1002")?.children[2].textContent);
    step("A: bag count updated by BagUpdatePush", pouch === "1", `1002 × ${pouch}`);

    await a.$eval("#gmCmd", (el) => { el.value = "addexp 150"; });
    await a.click("#btnGm");
    await waitText(a, "#pLevel", "return t === 'Lv.2'");
    step("A: GM 'addexp 150' → level up to Lv.2", true, await text(a, "#gmOut"));

    // ---------------------------------------------------------------- 4. second browser (B)
    const b = await openConsole(browser, "B");
    await loginAndEnter(b, "bob_" + suffix, "Bob");
    await waitText(b, "#nearby", "return t.includes('Alice')");
    step("B sees A on the map (AOI enter push)", true, await text(b, "#nearby"));
    await waitText(a, "#nearby", "return t.includes('Bob')");
    step("A sees B on the map", true, await text(a, "#nearby"));

    await a.$eval("#chatText", (el) => { el.value = "hello from Alice"; });
    await a.click("#btnChat");
    await waitText(b, "#chatList", "return t.includes('Alice:') && t.includes('hello from Alice')");
    step("B receives A's world chat", true);

    const posBefore = await text(a, "#pPos");
    await a.evaluate(() => document.activeElement?.blur()); // keyboard focus away from inputs
    await a.keyboard.press("d");
    await waitText(a, "#pPos", "return t !== arg", posBefore);
    const posAfter = await text(a, "#pPos");
    step("A moves with the keyboard", true, `${posBefore} → ${posAfter}`);
    await waitText(b, "#nearby", "return t.includes('Alice (520, 500)')");
    step("B sees A's new position (move push)", true, await text(b, "#nearby"));

    // click on the map: move toward the clicked point
    const box = await (await a.$("#map")).boundingBox();
    const pos2 = await text(a, "#pPos");
    await a.mouse.click(box.x + box.width * 0.6, box.y + box.height * 0.5);
    await waitText(a, "#pPos", "return t !== arg", pos2);
    step("A moves by clicking the map", true, `${pos2} → ${await text(a, "#pPos")}`);
    await a.screenshot({ path: `${OUT}/2-player-a.png`, fullPage: true });
    await b.screenshot({ path: `${OUT}/3-player-b.png`, fullPage: true });

    // ---------------------------------------------------------------- 5. duplicate login
    const c = await openConsole(browser, "C");
    await loginAndEnter(c, accA, "ignored");
    step("C logs in to A's account and gets A's character", (await text(c, "#pName")) === "Alice" && (await text(c, "#pLevel")) === "Lv.2");
    await waitState(a, "disconnected");
    const banner = await text(a, "#banner");
    step("A is kicked and shows the reason", banner.includes("KICK_DUPLICATE_LOGIN"), banner);
    const bagAfterKick = await text(a, "#bagBody");
    step("A's views reset after the kick (bag shows 'not in game')", bagAfterKick === "尚未进入游戏", bagAfterKick);
    step("A's character card cleared", await a.$eval("#playerCard", (el) => el.hidden));
    await waitText(b, "#nearby", "return t.includes('Alice')");
    step("B still sees Alice (now controlled by C)", true);
    await a.screenshot({ path: `${OUT}/4-kicked.png`, fullPage: true });

    // ---------------------------------------------------------------- 6. errors and language
    await c.$eval("#gmCmd", (el) => { el.value = "nosuchcommand"; });
    await c.click("#btnGm");
    await c.waitForSelector("#toast.err:not([hidden])", { timeout: 5000 });
    const toast = await text(c, "#toast");
    step("server error shown as a readable toast", toast.includes("ERR_GM_BAD_COMMAND"), toast);

    await c.click("#btnLang");
    const titleEn = await text(c, ".brand span:last-child");
    const pillEn = await text(c, "#statusPill");
    step("language switch to English", titleEn === "Game Server Web Console" && pillEn === "In game", `${titleEn} / ${pillEn}`);
    await c.screenshot({ path: `${OUT}/5-english.png`, fullPage: true });

    // narrow screen: no horizontal scroll
    await c.setViewport({ width: 390, height: 900 });
    const overflow = await c.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
    step("mobile width (390px) has no horizontal scroll", overflow <= 0, `overflow ${overflow}px`);
    await c.screenshot({ path: `${OUT}/6-mobile.png`, fullPage: true });

    step("no JavaScript errors in any page", pageErrors.length === 0, pageErrors.join(" | "));
  } catch (e) {
    console.log("ERROR " + e.message);
    if (pageErrors.length) console.log("page errors: " + pageErrors.join(" | "));
    process.exitCode = 1;
  } finally {
    await browser.close();
    const failed = results.filter((r) => !r.ok).length;
    console.log(`\n${results.length - failed}/${results.length} checks passed${process.exitCode ? " — FAILED" : ""}`);
    fs.writeFileSync(`${OUT}/results.json`, JSON.stringify({ results, pageErrors }, null, 2));
  }
})();
