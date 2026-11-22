// Clicks every safe button on every page and reports problems a page-load check never sees:
// JS errors, failed API calls, junk text (undefined/NaN/null/[object Object]), overflow,
// unstyled buttons, drawers or consoles that open empty.
//
// Usage: [BASE=http://127.0.0.1:7767] PLAYWRIGHT=/path/to/playwright-core TOKEN=<token from the Open link> node scripts/ui-crawl.js [width] [dark] [page,page]
// Needs a running dashboard on 127.0.0.1:7766, started with COLIMA_DASHBOARD_NO_TERMINAL=1 so no
// button can open Terminal windows, and Google Chrome. Run it before every release, once
// with a busy profile and once with a quiet one (no containers logging errors), at 1280 and 390.
const { chromium } = require(process.env.PLAYWRIGHT || "playwright-core");
const T = process.env.TOKEN || "";
const BASE = process.env.BASE || "http://127.0.0.1:7766"; // another port for a second, throwaway dashboard
if (!T) { console.error("set TOKEN to the token in the dashboard's Open link"); process.exit(2); }
const W = Number(process.argv[2] || 1280), dark = process.argv[3] === "dark";
const only = process.argv[4] ? process.argv[4].split(",") : null;
const PAGES = ["general", "monitor", "containers", "images", "volumes", "ports", "shells", "runs", "builds", "errors", "report", "security", "snapshots", "kubernetes", "troubleshooting", "diagnostics", "preferences"];
const UNSAFE = /terminal|ssh|osascript|open in|remove|delete|prune|kill|reset|force|uninstall|wipe|purge|stop|restart|pause|roll|restore|rebuild|start|run|resume|apply|save|create|update|upgrade|install|deploy|build|pull|push|scale|turn|fix|enable|disable|add|new|set|trust|import|forward|recalc|rate|scan|calculate|snapshot|sign|log ?out|change|switch|send|clean|reclaim|shrink|drain|cordon|evict|exec|test|benchmark|check now|retry|mute|unmute|dismiss|clear|copy|download|export|profile/i;
(async () => {
  const b = await chromium.launch({ channel: "chrome" });
  const ctx = await b.newContext({ viewport: { width: W, height: 900 }, colorScheme: dark ? "dark" : "light" });
  const p = await ctx.newPage();
  const found = [];
  let where = "";
  const add = (kind, msg) => { const k = `${where} | ${kind} | ${msg}`; if (!found.includes(k)) found.push(k); };
  p.on("pageerror", (e) => add("JS", e.message));
  p.on("console", (m) => { if (m.type() === "error" && !/Failed to load resource/.test(m.text())) add("console", m.text().slice(0, 160)); });
  p.on("response", (r) => { const u = r.url(); if (u.includes("/api/") && r.status() >= 400) add("HTTP " + r.status(), u.replace(/^.*\/api/, "/api").slice(0, 120)); });
  const scan = async () => {
    const r = await p.evaluate(() => {
      const out = [];
      const vis = (e) => e.offsetParent !== null || getComputedStyle(e).position === "fixed";
      const txt = document.body.innerText;
      for (const bad of ["undefined", "NaN", "[object Object]", "Invalid Date", ": null", "null ago", "(null)"]) if (txt.includes(bad)) {
        const i = txt.indexOf(bad); out.push(["junk", JSON.stringify(txt.slice(Math.max(0, i - 50), i + 40))]);
      }
      if (document.documentElement.scrollWidth > innerWidth + 1) out.push(["overflow", `${document.documentElement.scrollWidth}>${innerWidth}`]);
      for (const btn of document.querySelectorAll("button")) {
        if (!vis(btn)) continue;
        const s = getComputedStyle(btn);
        if (s.backgroundColor === "rgb(255, 255, 255)" && !btn.closest(".seg,nav,.statusbar,.xmenu-pop") && !btn.matches("button.link,.noact")) out.push(["plain-button", (btn.textContent || btn.getAttribute("aria-label") || "?").trim().slice(0, 40)]);
        const r = btn.getBoundingClientRect();
        if (r.right > innerWidth + 1) out.push(["button-off-screen", btn.textContent.trim().slice(0, 40)]);
      }
      const d = document.querySelector("#drawer");
      if (d && !d.hidden && d.offsetParent !== null) {
        const body = d.querySelector(".drawer-body, #drawerbody") || d;
        if (body.innerText.trim().length < 5) out.push(["empty-drawer", d.querySelector("h2,h3,#drawertitle")?.innerText || "?"]);
      }
      const jp = document.querySelector("#jobpanel");
      if (jp && !jp.hidden && !document.querySelector("#joblog").innerText.trim()) out.push(["empty-console", document.querySelector("#jobtitle").innerText]);
      return out;
    });
    for (const [k, m] of r) add(k, m);
  };
  for (const pg of only || PAGES) {
    where = pg;
    await p.goto(`${BASE}/?token=${T}#${pg}`);
    await p.waitForTimeout(4000);
    await scan();
    const labels = await p.evaluate(() => [...document.querySelectorAll("#main button, #main a.pill-link")].filter((e) => e.offsetParent !== null).map((e) => (e.textContent || e.getAttribute("aria-label") || "").trim()).filter(Boolean));
    const seen = new Set();
    for (const lab of labels) {
      if (UNSAFE.test(lab) || seen.has(lab) || seen.size > 25) continue;
      seen.add(lab);
      where = `${pg} > "${lab.slice(0, 30)}"`;
      try {
        if (!p.url().endsWith("#" + pg)) { await p.goto(`${BASE}/?token=${T}#${pg}`); await p.waitForTimeout(3000); }
        const el = p.locator("#main button:visible, #main a.pill-link:visible").filter({ hasText: new RegExp("^\\s*" + lab.replace(/[.*+?^${}()|[\]\\]/g, "\\$&") + "\\s*$") }).first();
        if (!(await el.count())) continue;
        await el.click({ timeout: 2000 });
        await p.waitForTimeout(1500);
        await scan();
        if (await p.locator("#drawerclose:visible").count()) await p.click("#drawerclose");
        await p.keyboard.press("Escape");
        await p.waitForTimeout(300);
      } catch (e) { add("click-failed", e.message.split("\n")[0].slice(0, 100)); }
    }
  }
  console.log(found.join("\n") || "nothing found");
  await b.close();
  process.exitCode = found.length ? 1 : 0; // CI fails on any finding
})();
