// Exports and reports: CSV and JSON downloads for every table and chart, the support bundle
// button, the Report page and the Prometheus switch. Loaded before app.js; everything here
// runs only when app.js calls it, so it can use app.js's helpers (el, api, P, toast, ...).
"use strict";

// ---------- Exports ----------

// exportStamp is the local time as YYYYMMDD-HHMM, for file names.
function exportStamp(d = new Date()) {
  const p = (n) => String(n).padStart(2, "0");
  return `${d.getFullYear()}${p(d.getMonth() + 1)}${p(d.getDate())}-${p(d.getHours())}${p(d.getMinutes())}`;
}

// exportName is <profile>-<what>-<YYYYMMDD-HHMM>.<ext>, the same shape the server uses.
function exportName(what, ext) {
  return `${state.profile || "colima"}-${what}-${exportStamp()}.${ext}`;
}

// csvCell quotes a value when it holds a comma, quote, line break or edge spaces.
function csvCell(v) {
  if (v === null || v === undefined) return "";
  const s = typeof v === "object" ? JSON.stringify(v) : String(v);
  return /[",\r\n]|^\s|\s$/.test(s) ? `"${s.replace(/"/g, '""')}"` : s;
}

// toCSV turns an array of flat objects into CSV. Columns are every key, in the order first seen.
function toCSV(rows) {
  const cols = [];
  const seen = new Set();
  for (const r of rows) for (const k of Object.keys(r)) if (!seen.has(k)) { seen.add(k); cols.push(k); }
  const lines = [cols.map(csvCell).join(",")];
  for (const r of rows) lines.push(cols.map((c) => csvCell(r[c])).join(","));
  return lines.join("\r\n") + "\r\n";
}

function saveBlob(name, blob) {
  const a = el("a", { href: URL.createObjectURL(blob), download: name });
  document.body.append(a);
  a.click();
  setTimeout(() => { URL.revokeObjectURL(a.href); a.remove(); }, 1000);
}

// saveURL downloads a same-origin URL (the session cookie goes with it).
function saveURL(url, name) {
  const a = el("a", { href: url, download: name || "" });
  document.body.append(a);
  a.click();
  setTimeout(() => a.remove(), 1000);
}

// exportMenu is a small "Export" menu with CSV and JSON. source is either a function returning
// an array of flat objects (sync or async), or { url } for a server route that answers
// ?format=csv and ?format=json with every record (history ranges can be large).
function exportMenu(what, source, label = "Export") {
  const pick = async (fmt) => {
    menu.open = false;
    try {
      if (typeof source === "function") {
        const rows = await source();
        if (!rows || !rows.length) { toast("Nothing to export yet"); return; }
        saveBlob(exportName(what, fmt), fmt === "csv"
          ? new Blob([toCSV(rows)], { type: "text/csv" })
          : new Blob([JSON.stringify(rows, null, 2) + "\n"], { type: "application/json" }));
      } else {
        const u = source.url + (source.url.includes("?") ? "&" : "?") + "format=" + fmt;
        saveURL(u, exportName(what, fmt));
      }
    } catch (e) { toast(e.message, true); }
  };
  const menu = el("details.xmenu",
    el("summary", { "aria-label": `${label} ${what}`, title: `Download ${what} as CSV or JSON` }, label),
    el("div.xmenu-pop",
      el("button.ghost.mini", { type: "button", onclick: () => pick("csv") }, "CSV"),
      el("button.ghost.mini", { type: "button", onclick: () => pick("json") }, "JSON")));
  return menu;
}

// seriesRows flattens a chart's series into one row per sample time: what the chart shows.
function seriesRows(times, series) {
  return times.map((t, i) => {
    const row = { time: new Date(t).toISOString() };
    for (const s of series) row[s.name] = s.values[i] ?? "";
    return row;
  });
}

// containerExportRows flattens the Containers page's list.
function containerExportRows(list) {
  return list.map((c) => ({
    id: c.Id, name: (c.Names?.[0] || "").replace(/^\//, ""), image: c.Image, state: c.State, status: c.Status,
    project: c.Labels?.["com.docker.compose.project"] || "", service: c.Labels?.["com.docker.compose.service"] || "",
    ports: (c.Ports || []).map((p) => p.PublicPort ? `${p.IP || ""}:${p.PublicPort}->${p.PrivatePort}/${p.Type}` : `${p.PrivatePort}/${p.Type}`).join(" "),
    created: c.Created ? new Date(c.Created * 1000).toISOString() : "",
  }));
}

// One open menu at a time; a click elsewhere or Escape closes it.
document.addEventListener("click", (e) => {
  for (const d of document.querySelectorAll("details.xmenu[open]")) if (!d.contains(e.target)) d.open = false;
});
document.addEventListener("keydown", (e) => {
  if (e.key === "Escape") for (const d of document.querySelectorAll("details.xmenu[open]")) d.open = false;
});

// ---------- Support bundle ----------

function supportBundleCard() {
  const btn = el("button", { type: "button" }, "Download support bundle");
  btn.addEventListener("click", () => {
    saveURL(`${P()}/support-bundle`, exportName("support-bundle", "zip"));
    btn.disabled = true;
    btn.textContent = "Preparing…";
    toast("Collecting the support bundle; the download starts in a few seconds");
    setTimeout(() => { btn.disabled = false; btn.textContent = "Download support bundle"; }, 8000);
  });
  return el("div.card", el("h2", "Support bundle"),
    el("p.muted", `One zip for whoever helps you with profile ${state.profile}: these checks, Colima's version, profiles and settings, Docker version and info, the last 2000 lines of each VM log, recent alerts, the dashboard's version and settings, and Kubernetes pods and events when k3s is on.`),
    el("p.small.muted", "Your home folder, user name and session token are replaced, and anything that looks like a secret (KEY, TOKEN, SECRET, PASSWORD values, registry auths, kubeconfig keys) is removed. README.txt inside lists what was taken out. Look it over before you share it."),
    btn);
}

// ---------- Report ----------

function reportStat(value, label, title, tone) {
  return el(`div.stat${tone ? "." + tone : ""}`, { title: title || "" }, el("strong", value), el("span", label));
}

function jobRows(list, empty) {
  if (!list.length) return el("p.empty.small", empty);
  return el("div.table-wrap", el("table.rtable",
    el("thead", el("tr", ["Job", "Runs", "Failed", "Passed", "Median", "Slowest 10%"].map((x) => el("th", x)))),
    el("tbody", list.map((j) => el("tr",
      el("td", el("strong.mono", j.job)),
      el("td.mono", String(j.runs)),
      el("td.mono", String(j.failed)),
      el("td.mono", pctRate(j.passRate)),
      el("td.mono.nowrap", dur(j.median)),
      el("td.mono.nowrap", dur(j.p90)))))));
}

function usageRow(label, s, fmt) {
  return el("tr", el("td", label),
    el("td.mono", s.n ? fmt(s.peak) : "–"),
    el("td.mono", s.n ? fmt(s.avg) : "–"),
    el("td.small.muted", s.n ? `${s.n} samples` : "not sampled"));
}

function signedBytes(n) { return n < 0 ? `-${bytes(-n)}` : `+${bytes(n)}`; }

async function renderReport(main) {
  const days = [1, 7, 30].includes(Number(safeGet("reportdays"))) ? Number(safeGet("reportdays")) : 7;
  const md = el("button.ghost", { type: "button", onclick: () => saveURL(`${P()}/report?days=${days}&format=md`, exportName(`report-${days}d`, "md")) }, "Download as Markdown");
  const print = el("button", { type: "button", onclick: () => window.print() }, "Print or save as PDF");
  const head = el("div.head",
    el("div", el("h1", "Report"), el("p.muted", `What happened in profile ${state.profile}: tests, builds, disk, cost and uptime, from the history kept on this Mac.`)),
    el("div.toolbar.report-tools", daysSeg(days, (d) => { safeSet("reportdays", String(d)); render(); }), md, print));
  main.replaceChildren(head, el("div.card", el("p.muted", "Loading…")));
  const { report: r, recording } = await api("GET", `${P()}/report?days=${days}`);
  const t = r.tests, b = r.builds, d = r.disk, u = r.usage;
  const when = (iso) => new Date(iso).toLocaleString([], { day: "numeric", month: "short", year: "numeric", hour: "2-digit", minute: "2-digit" });
  const pctFmt = (v) => `${v.toFixed(v < 10 ? 1 : 0)}%`;

  const title = el("div.report-title", el("h2", `Colima Dashboard report: ${r.profile}`),
    el("p.muted.small", `Last ${days === 1 ? "day" : `${days} days`}, ${when(r.from)} to ${when(r.to)}`));
  const stats = el("div.stat-row",
    reportStat(pctRate(r.uptime.fraction), "running", dur(r.uptime.seconds) + " seen running; time with the dashboard closed counts as not running"),
    reportStat(pctRate(t.passRate), "tests passed", `of ${t.own} runs that ended on their own`, t.passRate >= 0 && t.passRate < 0.8 ? "bad" : ""),
    reportStat(String(t.runs), "runs", `${t.passed} passed, ${t.failed} failed, ${t.oom} out of memory`),
    reportStat(String(t.flaky.length), "flaky jobs", "passed and failed in their last 10 runs", t.flaky.length ? "warn" : ""),
    reportStat(String(b.builds), "builds", `${b.failed} failed`),
    reportStat(String(b.failed), "failed builds", "", b.failed ? "bad" : ""),
    reportStat(d.end ? signedBytes(d.growth) : "–", "VM disk growth", d.end ? `${bytes(d.end)} on the Mac now` : "no disk samples"),
    reportStat(String(t.oom), "out of memory", "containers killed for memory", t.oom ? "bad" : ""));

  const tests = el("div.card.viz", el("div.viz-head", el("h2", "Tests and runs"),
    el("span.muted.small", `${t.jobs} jobs · ${t.own} ended on their own`)),
  el("div.report-body",
    el("h3", "Flaky jobs"), jobRows(t.flaky, "None: no job both passed and failed in its last 10 runs."),
    el("h3", "Most failing jobs"), jobRows(t.failing, "No job failed."),
    el("h3", "Slowest jobs"), jobRows(t.slowest, "No runs ended on their own.")));

  const builds = el("div.card.viz", el("div.viz-head", el("h2", "Builds"),
    el("span.muted.small", b.builds ? `median ${dur(b.median)} · ${dur(b.total)} in all` : "none recorded")),
  el("div.report-body",
    el("h3", "Slowest images"),
    b.slowest.length ? el("div.table-wrap", el("table.rtable",
      el("thead", el("tr", ["Image", "Builds", "Failed", "Median", "Longest"].map((x) => el("th", x)))),
      el("tbody", b.slowest.map((x) => el("tr",
        el("td", el("strong.mono", x.name || "(unnamed)")),
        el("td.mono", String(x.builds)), el("td.mono", String(x.failed)),
        el("td.mono.nowrap", dur(x.median)), el("td.mono.nowrap", dur(x.max)))))))
      : el("p.empty.small", "No completed builds.")));

  const f = d.forecast;
  let forecast = "A forecast needs a day of history.";
  if (f && f.reliable) forecast = f.days >= 0 ? `Growing about ${bytes(f.perDay)} a day: full in about ${Math.round(f.days)} days.` : "Not growing.";
  const disk = el("div.card.viz", el("div.viz-head", el("h2", "Disk"), el("span.muted.small", "the VM's disk image on your Mac")),
    el("div.report-body", d.end
      ? el("dl.kv", el("dt", "At the start"), el("dd", bytes(d.start)), el("dt", "At the end"), el("dd", bytes(d.end)),
        el("dt", "Change"), el("dd", signedBytes(d.growth)), el("dt", "Disk size"), el("dd", bytes(d.size)),
        el("dt", "Forecast"), el("dd", f ? forecast : "Not enough samples."))
      : el("p.empty.small", "No disk samples in this window.")));

  const usage = el("div.card.viz", el("div.viz-head", el("h2", "CPU and memory"), el("span.muted.small", "5 minute samples")),
    el("div.report-body", el("div.table-wrap", el("table.rtable",
      el("thead", el("tr", ["", "Peak", "Average", ""].map((x) => el("th", x)))),
      el("tbody",
        usageRow("Mac CPU used by the VM (% of one core)", u.hostCpu, pctFmt),
        usageRow("Mac memory used by the VM", u.hostMem, bytes),
        usageRow("CPU inside the VM (while the Monitor was open)", u.guestCpu, pctFmt),
        usageRow("Memory inside the VM (while the Monitor was open)", u.guestMem, bytes),
        usageRow("Running containers", u.running, (v) => v.toFixed(v % 1 ? 1 : 0)))))));

  const oom = el("div.card.viz", el("div.viz-head", el("h2", "Out of memory"), el("span.muted.small", `${t.oom} in this window`)),
    el("div.report-body", t.oomEvents.length
      ? el("ul.report-list", t.oomEvents.map((e) => el("li", el("span.mono.small", when(e.t)), " ", el("strong", e.name), el("span.muted.small", ` ${e.job}`))))
      : el("p.empty.small", "No container was killed for memory.")));

  main.replaceChildren(head, el("div.report", title, stats, el("div.viz-grid", tests, builds), el("div.viz-grid", disk, usage), oom,
    recording ? el("p.small.muted", "Computed from the runs, builds and 5 minute samples Colima Dashboard keeps for 30 days. Uptime counts the time the profile was running while the dashboard was recording.")
      : el("div.notice", `Profile ${state.profile} is not running, so nothing new is being recorded.`)));
}

// ---------- Prometheus ----------

function prometheusCard() {
  const box = el("input", { type: "checkbox", disabled: true });
  const note = el("p.small.muted", "Off: /metrics answers only this signed-in browser.");
  const sync = (p) => {
    box.checked = p.enabled;
    box.disabled = !p.available;
    note.replaceChildren(p.enabled
      ? el("span", "On: a local Prometheus can scrape ", el("code", p.url), " without signing in. It serves cached figures and starts nothing on a scrape; container figures appear while the Monitor samples.")
      : el("span", "Off: ", el("code", "/metrics"), " answers only this signed-in browser."));
  };
  api("GET", "/api/prometheus").then(sync).catch((e) => note.replaceChildren(el("span.bad-text", e.message)));
  box.addEventListener("change", async () => {
    try { sync(await api("POST", "/api/prometheus", { enabled: box.checked })); toast(box.checked ? "Prometheus metrics endpoint on" : "Prometheus metrics endpoint off"); }
    catch (e) { box.checked = !box.checked; toast(e.message, true); }
  });
  return el("section.card", el("h2", "Prometheus"),
    el("label.check", box, "Prometheus metrics endpoint"),
    note);
}
