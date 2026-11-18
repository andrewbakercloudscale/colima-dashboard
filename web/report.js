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

// slowerPill flags a job or image whose last 5 runs are a fifth or more slower than the 20 before.
function slowerPill(ratio) {
  if (!(ratio >= 1.2)) return null;
  return el("span.pill.warn.slow-pill", { title: "median of the last 5 against the 20 before" }, `${Math.round((ratio - 1) * 100)}% slower`);
}

function trendCell(trend) {
  return el("td.trend-cell", trend?.length ? Charts.runBars(trend.map((p) => ({ t: Date.parse(p.t), seconds: p.seconds, outcome: p.outcome })), dur) : el("span.muted.small", "–"));
}

function jobRows(list, empty) {
  if (!list.length) return el("p.empty.small", empty);
  return el("div.table-wrap", el("table.rtable",
    el("thead", el("tr", ["Job", "Last runs", "Runs", "Failed", "Passed", "Median", "Slowest 10%"].map((x) => el("th", x)))),
    el("tbody", list.map((j) => el("tr",
      el("td", el("strong.mono", j.job), slowerPill(j.slower)),
      trendCell(j.trend),
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

// shortDur fits a chart's y axis: 0, 45 s, 2.5 min, 1.5 h.
function shortDur(sec) {
  if (!(sec > 0)) return "0";
  if (sec < 1) return `${Math.round(sec * 1000)} ms`;
  if (sec < 90) return `${Math.round(sec)} s`;
  if (sec < 5400) return `${+(sec / 60).toFixed(1)} min`;
  return `${+(sec / 3600).toFixed(1)} h`;
}

// vizCard is a report card: a heading, a note, an Export menu and the chart.
function vizCard(title, note, what, rows, ...body) {
  return el("div.card.viz", el("div.viz-head", el("h2", title),
    el("span.viz-tools", note ? el("span.muted.small", note) : null, what ? exportMenu(what, rows) : null)), ...body);
}

const OOM_COLOR = Charts.OUTCOME_COLORS.oom;

// regressionText is one sentence about a job or image that got slower or faster.
function regressionText(g, than) {
  const slower = g.ratio > 1;
  const pct = Math.round((slower ? g.ratio - 1 : 1 - g.ratio) * 100);
  return { pct, slower, text: `${pct}% ${slower ? "slower" : "faster"} ${than}` };
}

// goToUpdates opens General and scrolls to its Updates list once it is drawn.
function goToUpdates() {
  location.hash = "#general";
  let tries = 0;
  const t = setInterval(() => {
    const w = document.getElementById("welcome");
    if (w || ++tries > 30) { clearInterval(t); w?.querySelector(".upd-list")?.scrollIntoView({ block: "start", behavior: "smooth" }); }
  }, 100);
}

async function renderReport(main) {
  const days = [1, 7, 30].includes(Number(safeGet("reportdays"))) ? Number(safeGet("reportdays")) : 7;
  const md = el("button.ghost", { type: "button", onclick: () => saveURL(`${P()}/report?days=${days}&format=md`, exportName(`report-${days}d`, "md")) }, "Download as Markdown");
  const print = el("button", { type: "button", onclick: () => window.print() }, "Print or save as PDF");
  const head = el("div.head",
    el("div", el("h1", "Report"), el("p.muted", `What happened in profile ${state.profile}: tests, builds, errors, speed, disk, cost and what is out of date, from the history kept on this Mac.`)),
    el("div.toolbar.report-tools", daysSeg(days, (d) => { safeSet("reportdays", String(d)); render(); }), md, print));
  main.replaceChildren(head, el("div.card", el("p.muted", "Loading…")));
  loadAdvice().catch(() => { /* Fix buttons are extra */ });
  const { report: r, recording } = await api("GET", `${P()}/report?days=${days}`);
  const t = r.tests, b = r.builds, d = r.disk, u = r.usage, tr = r.trends, o = r.outdated;
  const when = (iso) => new Date(iso).toLocaleString([], { day: "numeric", month: "short", year: "numeric", hour: "2-digit", minute: "2-digit" });
  const pctFmt = (v) => `${v.toFixed(v < 10 ? 1 : 0)}%`;
  const per = tr.bucket < 86400 ? "hour" : "day";
  const times = tr.buckets.map((x) => Date.parse(x.t));
  const width = tr.bucket * 1000;
  const iso = (ms) => new Date(ms).toISOString();
  const slower = tr.regressions.filter((g) => g.ratio > 1);
  const errors = t.failed + t.oom + b.failed;

  const title = el("div.report-title", el("h2", `Colima Dashboard report: ${r.profile}`),
    el("p.muted.small", `Last ${days === 1 ? "day" : `${days} days`}, ${when(r.from)} to ${when(r.to)}`));
  const stats = el("div.stat-row",
    reportStat(pctRate(r.uptime.fraction), "running", dur(r.uptime.seconds) + " seen running; time with the dashboard closed counts as not running"),
    reportStat(pctRate(t.passRate), "tests passed", `of ${t.own} runs that ended on their own`, t.passRate >= 0 && t.passRate < 0.8 ? "bad" : ""),
    reportStat(String(t.runs), "runs", `${t.passed} passed, ${t.failed} failed, ${t.oom} out of memory`),
    reportStat(String(errors), "errors", `${t.failed} failed runs, ${t.oom} out of memory, ${b.failed} failed builds`, errors ? "bad" : ""),
    reportStat(String(slower.length), "got slower", `jobs or images whose median time grew by a quarter or more ${tr.than}`, slower.length ? "warn" : ""),
    reportStat(String(t.flaky.length), "flaky jobs", "passed and failed in their last 10 runs", t.flaky.length ? "warn" : ""),
    reportStat(String(b.builds), "builds", `${b.failed} failed`),
    reportStat(String(t.oom), "out of memory", "containers killed for memory", t.oom ? "bad" : ""),
    reportStat(d.end ? signedBytes(d.growth) : "–", "VM disk growth", d.end ? `${bytes(d.end)} on the Mac now` : "no disk samples"),
    o?.toolsKnown || o?.imagesKnown ? reportStat(String((o.behind || 0) + (o.oldCount || 0)), "out of date", `${o.behind} tools behind, ${o.oldCount} images older than ${o.oldDays} days`, o.behind || o.oldCount ? "warn" : "") : null);

  // ---- Performance changes ----
  const regRows = () => tr.regressions.map((g) => ({ kind: g.kind, name: g.name, median_now_s: g.now, median_before_s: g.before, ratio: g.ratio, runs_now: g.nowRuns, runs_before: g.beforeRuns, compared: tr.than }));
  const changes = vizCard("Performance changes", `median time, this period ${tr.than}`, `report-changes-${days}d`, regRows,
    tr.regressions.length
      ? el("ul.reg-list", tr.regressions.map((g) => {
        const x = regressionText(g, tr.than);
        return el(`li.${x.slower ? "slower" : "faster"}`,
          el("span.reg-arrow", { "aria-hidden": "true" }, x.slower ? "▲" : "▼"),
          el("span.reg-main", el("strong.mono", g.name), el("span.pill.x", g.kind === "build" ? "build" : "test"),
            el("span.reg-pct", x.text)),
          el("span.reg-detail.muted.small", `median ${dur(g.now)}, was ${dur(g.before)} · ${g.nowRuns} and ${g.beforeRuns} runs`));
      }))
      : el("p.empty.small.report-pad", `No job or image changed its median time by a quarter or more ${tr.than}. Jobs need 3 runs on each side and a change of at least a second.`));

  // ---- Activity ----
  const actRows = () => tr.buckets.map((x) => ({ start: x.t, passed: x.passed, failed: x.failed, out_of_memory: x.oom, stopped: x.stopped, restarts: x.restarts, unhealthy: x.unhealthy, builds: x.builds, failed_builds: x.buildsFailed }));
  const runsChart = vizCard(`Runs per ${per}`, `${t.runs} runs · ${b.builds} builds`, `report-runs-per-${per}-${days}d`, actRows,
    Charts.barChart({ times, width, empty: "No runs in this window.", series: [
      { name: "passed", color: Charts.OUTCOME_COLORS.pass, values: tr.buckets.map((x) => x.passed) },
      { name: "failed", color: Charts.OUTCOME_COLORS.fail, values: tr.buckets.map((x) => x.failed) },
      { name: "out of memory", color: OOM_COLOR, values: tr.buckets.map((x) => x.oom) },
      { name: "stopped", color: Charts.OUTCOME_COLORS.stopped, values: tr.buckets.map((x) => x.stopped) },
    ] }));

  const rates = tr.buckets.map((x) => { const own = x.passed + x.failed + x.oom; return own ? (x.passed / own) * 100 : null; });
  const buildRates = tr.buckets.map((x) => x.builds ? ((x.builds - x.buildsFailed) / x.builds) * 100 : null);
  const passChart = vizCard("Pass rate", `tests and builds that succeeded, per ${per}`, `report-pass-rate-${days}d`,
    () => tr.buckets.map((x, i) => ({ start: x.t, test_pass_pct: rates[i] ?? "", build_success_pct: buildRates[i] ?? "" })),
    rates.some((v) => v !== null) || buildRates.some((v) => v !== null)
      ? Charts.timeChart({ times, domain: [times[0], times[times.length - 1]], yMax: 100, dots: true, wash: false, gapMs: width * 1.5, fmt: (v) => `${Math.round(v)}%`,
        series: [{ name: "tests passed", color: "var(--series-3)", values: rates }, { name: "builds succeeded", color: "var(--series-1)", values: buildRates }] })
      : el("p.empty.small.report-pad", "No runs or builds ended on their own in this window."));

  // ---- Errors ----
  const errChart = vizCard(`Errors per ${per}`, `${errors} in all`, `report-errors-per-${per}-${days}d`, actRows,
    Charts.barChart({ times, width, empty: "No errors in this window.", series: [
      { name: "failed runs", color: Charts.OUTCOME_COLORS.fail, values: tr.buckets.map((x) => x.failed) },
      { name: "out of memory", color: OOM_COLOR, values: tr.buckets.map((x) => x.oom) },
      { name: "failed builds", color: "var(--series-4)", values: tr.buckets.map((x) => x.buildsFailed) },
      { name: "restarts", color: "var(--series-1)", values: tr.buckets.map((x) => x.restarts) },
      { name: "unhealthy", color: "var(--series-5)", values: tr.buckets.map((x) => x.unhealthy) },
    ] }));

  const codeRows = () => [...tr.exitCodes.map((c) => ({ kind: "exit code", what: c.code, count: c.count, meaning: c.meaning, where: c.jobs.join(" ") })),
    ...tr.buildErrors.map((e) => ({ kind: "build error", what: e.reason, count: e.count, meaning: e.last, where: e.images.join(" ") }))];
  const why = vizCard("Why things failed", "exit codes and build errors", `report-failure-reasons-${days}d`, codeRows,
    el("div.report-body",
      el("h3", "Exit codes of failed runs"),
      tr.exitCodes.length ? Charts.hbars(tr.exitCodes.map((c) => ({ label: `exit ${c.code}`, sub: `${c.meaning} · ${c.jobs.join(", ")}`, value: c.count,
        color: c.code === 137 ? OOM_COLOR : Charts.OUTCOME_COLORS.fail, text: String(c.count) }))) : el("p.empty.small", "No run failed."),
      el("h3", "Build errors"),
      tr.buildErrors.length ? Charts.hbars(tr.buildErrors.map((e) => ({ label: e.reason, sub: e.images.join(", "), value: e.count, color: "var(--series-4)", text: String(e.count) })))
        : el("p.empty.small", "No build failed.")));

  const troubleRows = () => tr.trouble.map((x) => ({ job: x.job, runs: x.runs, failed: x.failed, out_of_memory: x.oom, restarts: x.restarts, unhealthy: x.unhealthy }));
  const trouble = vizCard("Most trouble", "failed, out of memory, restarted or unhealthy", `report-trouble-${days}d`, troubleRows,
    el("div.report-body", tr.trouble.length
      ? [Charts.hbars(tr.trouble.map((x) => ({ label: x.job, sub: [x.failed && `${x.failed} failed`, x.oom && `${x.oom} out of memory`, x.restarts && `${x.restarts} restarts`, x.unhealthy && `${x.unhealthy} unhealthy`].filter(Boolean).join(" · ") + ` of ${x.runs} runs`,
        text: String(x.failed + x.oom + x.restarts + x.unhealthy), parts: [
          { name: "failed", value: x.failed, color: Charts.OUTCOME_COLORS.fail }, { name: "out of memory", value: x.oom, color: OOM_COLOR },
          { name: "restarts", value: x.restarts, color: "var(--series-1)" }, { name: "unhealthy", value: x.unhealthy, color: "var(--series-5)" }] }))),
      Charts.legend([{ label: "failed", color: Charts.OUTCOME_COLORS.fail }, { label: "out of memory", color: OOM_COLOR }, { label: "restarts", color: "var(--series-1)" }, { label: "unhealthy", color: "var(--series-5)" }])]
      : el("p.empty.small", "Nothing failed, ran out of memory, restarted or went unhealthy.")));

  // ---- Speed ----
  const durationCard = (title, list, slug, empty) => {
    let mode = safeGet("reportdurmode") === "p90" ? "p90" : "median";
    const holder = el("div");
    // a round top in the unit the axis shows: 2 min, not 16.7 min (1000 s)
    const niceDur = (v) => v > 5400 ? Charts.niceMax(v / 3600) * 3600 : v > 90 ? Charts.niceMax(v / 60) * 60 : Charts.niceMax(v);
    const draw = () => {
      const peak = Math.max(0, ...list.flatMap((x) => x[mode].map((v) => v ?? 0)));
      seg.querySelectorAll("button").forEach((x) => { x.className = x.dataset.m === mode ? "on" : "off"; });
      holder.replaceChildren(list.length ? Charts.timeChart({ times, domain: [times[0], times[times.length - 1]], dots: true, wash: false, gapMs: width * 1.5, fmt: dur, axisFmt: shortDur, yMax: niceDur(peak * 1.15),
        series: list.map((x, i) => ({ name: x.name, color: Charts.seriesColor(i), values: x[mode].map((v) => v ?? null) })) })
        : el("p.empty.small.report-pad", empty));
    };
    const seg = el("div.seg.seg-sm", [["median", "Median"], ["p90", "Slowest 10%"]].map(([m, l]) =>
      el("button", { type: "button", dataset: { m }, onclick: () => { mode = m; safeSet("reportdurmode", m); draw(); } }, l)));
    draw();
    const rows = () => tr.buckets.flatMap((x, i) => list.filter((s) => s.count[i]).map((s) => ({ start: x.t, name: s.name, runs: s.count[i], median_s: s.median[i], p90_s: s.p90[i] })));
    return vizCard(title, `per ${per}, busiest ${list.length || ""}`.trim(), slug, rows, el("div.viz-sub", seg), holder);
  };
  const testSpeed = durationCard("Test duration", tr.jobs, `report-test-duration-${days}d`, "No job ran twice on its own in this window.");
  const buildSpeed = durationCard("Build duration", tr.images, `report-build-duration-${days}d`, "No image was built twice in this window.");

  // ---- Tables ----
  const flatJobs = (list) => list.map((j) => ({ job: j.job, runs: j.runs, failed: j.failed, pass_rate: j.passRate < 0 ? "" : j.passRate, median_s: j.median, p90_s: j.p90, slower_ratio: j.slower || "" }));
  const tests = vizCard("Tests and runs", `${t.jobs} jobs · ${t.own} ended on their own`, `report-jobs-${days}d`,
    () => [...flatJobs(t.flaky).map((x) => ({ list: "flaky", ...x })), ...flatJobs(t.failing).map((x) => ({ list: "most failing", ...x })), ...flatJobs(t.slowest).map((x) => ({ list: "slowest", ...x }))],
    el("div.report-body",
      el("h3", "Flaky jobs"), jobRows(t.flaky, "None: no job both passed and failed in its last 10 runs."),
      el("h3", "Most failing jobs"), jobRows(t.failing, "No job failed."),
      el("h3", "Slowest jobs"), jobRows(t.slowest, "No runs ended on their own.")));

  const builds = vizCard("Builds", b.builds ? `median ${dur(b.median)} · ${dur(b.total)} in all` : "none recorded", `report-builds-${days}d`,
    () => b.slowest.map((x) => ({ image: x.name, builds: x.builds, failed: x.failed, median_s: x.median, longest_s: x.max, slower_ratio: x.slower || "" })),
    el("div.report-body",
      el("h3", "Slowest images"),
      b.slowest.length ? el("div.table-wrap", el("table.rtable",
        el("thead", el("tr", ["Image", "Last builds", "Builds", "Failed", "Median", "Longest"].map((x) => el("th", x)))),
        el("tbody", b.slowest.map((x) => el("tr",
          el("td", el("strong.mono", x.name || "(unnamed)"), slowerPill(x.slower)),
          trendCell(x.trend),
          el("td.mono", String(x.builds)), el("td.mono", String(x.failed)),
          el("td.mono.nowrap", dur(x.median)), el("td.mono.nowrap", dur(x.max)))))))
        : el("p.empty.small", "No completed builds.")));

  // ---- Resources ----
  const ut = tr.usage.map((x) => Date.parse(x.t));
  const udom = [ut[0], ut[ut.length - 1] + tr.usageBucket * 1000];
  const ugap = tr.usageBucket * 1000 * 2.5;
  const has = (k) => tr.usage.some((x) => x[k] !== null && x[k] !== undefined);
  const usageRows = () => tr.usage.filter((x) => x.hostCpu !== null || x.diskMac !== null).map((x) => ({ start: x.t, mac_cpu_avg_pct: x.hostCpu ?? "", mac_cpu_peak_pct: x.hostCpuPeak ?? "",
    mac_mem_avg_bytes: x.hostMem ?? "", vm_cpu_avg_pct: x.guestCpu ?? "", vm_mem_avg_bytes: x.guestMem ?? "", disk_on_mac_bytes: x.diskMac ?? "", running_avg: x.running ?? "" }));
  const ubucket = tr.usageBucket >= 3600 ? `${tr.usageBucket / 3600} hour` : `${tr.usageBucket / 60} minute`;
  const cpuSeries = [{ name: "average", color: "var(--series-1)", values: tr.usage.map((x) => x.hostCpu) },
    { name: "peak", color: "var(--series-2)", values: tr.usage.map((x) => x.hostCpuPeak) }];
  if (has("guestCpu")) cpuSeries.push({ name: "inside the VM", color: "var(--series-3)", values: tr.usage.map((x) => x.guestCpu) });
  const cpuChart = vizCard("Mac CPU used by the VM", `% of one core, ${ubucket} buckets`, `report-cpu-${days}d`, usageRows,
    has("hostCpu") ? Charts.timeChart({ times: ut, domain: udom, gapMs: ugap, fmt: pctFmt, series: cpuSeries, empty: "No samples in this window." })
      : el("p.empty.small.report-pad", "No samples in this window."));
  const memSeries = [{ name: "Mac memory used by the VM", color: "var(--series-7)", values: tr.usage.map((x) => x.hostMem) }];
  if (has("guestMem")) memSeries.push({ name: "used inside the VM", color: "var(--series-3)", values: tr.usage.map((x) => x.guestMem) });
  const memChart = vizCard("Memory", `average per ${ubucket} bucket`, `report-memory-${days}d`, usageRows,
    has("hostMem") ? Charts.timeChart({ times: ut, domain: udom, gapMs: ugap, fmt: bytes, series: memSeries })
      : el("p.empty.small.report-pad", "No samples in this window."));

  const f = d.forecast;
  let forecast = "A forecast needs a day of history.";
  if (f && f.reliable) forecast = f.days >= 0 ? `Growing about ${bytes(f.perDay)} a day: full in about ${Math.round(f.days)} days.` : "Not growing.";
  // Disk use, with the forecast drawn dashed a quarter of the range into the future once it is reliable.
  let diskChart = el("p.empty.small.report-pad", "No disk samples in this window.");
  if (has("diskMac")) {
    const dt = [...ut], used = tr.usage.map((x) => x.diskMac), proj = used.map(() => null);
    let dom = udom;
    if (f && f.reliable && f.perDay > 0) {
      let li = used.length - 1;
      while (li > 0 && used[li] === null) li--;
      const ahead = days * 86400e3 / 4;
      proj[li] = used[li];
      dt.push(dt[dt.length - 1] + ahead);
      used.push(null);
      proj.push(Math.min(f.size || Infinity, used[li] + f.perDay * (dt[dt.length - 1] - dt[li]) / 86400e3));
      dom = [udom[0], dt[dt.length - 1]];
    }
    const series = [{ name: "on the Mac", color: "var(--series-4)", values: used }];
    if (proj.some((v) => v !== null)) series.push({ name: "forecast", color: "var(--series-4)", values: proj, dash: true });
    diskChart = Charts.timeChart({ times: dt, domain: dom, gapMs: Infinity, fmt: bytes, series });
  }
  const disk = vizCard("VM disk", "the VM's disk image on your Mac", `report-disk-${days}d`,
    () => tr.usage.filter((x) => x.diskMac !== null).map((x) => ({ start: x.t, disk_on_mac_bytes: x.diskMac })),
    diskChart,
    el("div.report-body", d.end
      ? el("dl.kv", el("dt", "At the start"), el("dd", bytes(d.start)), el("dt", "At the end"), el("dd", bytes(d.end)),
        el("dt", "Change"), el("dd", signedBytes(d.growth)), el("dt", "Disk size"), el("dd", bytes(d.size)),
        el("dt", "Forecast"), el("dd", f ? forecast : "Not enough samples."))
      : null));

  const usage = vizCard("CPU and memory", "5 minute samples", `report-usage-${days}d`,
    () => [["mac_cpu_pct", u.hostCpu], ["mac_mem_bytes", u.hostMem], ["vm_cpu_pct", u.guestCpu], ["vm_mem_bytes", u.guestMem], ["running_containers", u.running]]
      .map(([k, s]) => ({ metric: k, peak: s.n ? s.peak : "", average: s.n ? s.avg : "", samples: s.n })),
    el("div.report-body", el("div.table-wrap", el("table.rtable",
      el("thead", el("tr", ["", "Peak", "Average", ""].map((x) => el("th", x)))),
      el("tbody",
        usageRow("Mac CPU used by the VM (% of one core)", u.hostCpu, pctFmt),
        usageRow("Mac memory used by the VM", u.hostMem, bytes),
        usageRow("CPU inside the VM (while the Monitor was open)", u.guestCpu, pctFmt),
        usageRow("Memory inside the VM (while the Monitor was open)", u.guestMem, bytes),
        usageRow("Running containers", u.running, (v) => v.toFixed(v % 1 ? 1 : 0)))))));

  const seenJobs = new Set(), firstOf = (j) => !seenJobs.has(j) && seenJobs.add(j); // one Fix per job, on its newest event
  const oom = vizCard("Out of memory", `${t.oom} in this window`, null, null,
    el("div.report-body", t.oomEvents.length
      ? el("ul.report-list", t.oomEvents.map((e) => el("li", el("span.mono.small", when(e.t)), " ", el("strong", e.name), el("span.muted.small", ` ${e.job}`), " ", firstOf(e.job) ? fixSlot(adviceFor.job(e.job)) : null)))
      : el("p.empty.small", "No container was killed for memory.")));

  main.replaceChildren(head, el("div.report", title, stats, changes,
    el("div.viz-grid", runsChart, passChart),
    el("div.viz-grid", errChart, why),
    el("div.viz-grid", testSpeed, buildSpeed),
    tests, builds,
    el("div.viz-grid", trouble, oom),
    el("div.viz-grid", cpuChart, memChart),
    el("div.viz-grid", disk, usage),
    o ? outdatedCard(o) : null,
    recording ? el("p.small.muted", "Computed from the runs, builds and 5 minute samples Colima Dashboard keeps for 30 days. Uptime counts the time the profile was running while the dashboard was recording.")
      : el("div.notice", `Profile ${state.profile} is not running, so nothing new is being recorded.`)));
}

// outdatedCard: old and floating images, dangling layers, and tools behind their latest release.
function outdatedCard(o) {
  const age = (n) => n >= 365 ? `${(n / 365).toFixed(1)} years` : n === 1 ? "1 day" : `${n} days`;
  const rows = () => [
    ...o.old.map((i) => ({ kind: "old image", name: i.ref, id: i.id, age_days: i.ageDays, size_bytes: i.size, containers: i.containers })),
    ...o.latest.map((i) => ({ kind: ":latest image", name: i.ref, id: i.id, age_days: i.ageDays, size_bytes: i.size, containers: i.containers })),
    ...o.tools.map((x) => ({ kind: "tool", name: x.label, installed: x.current, latest: x.latest || "", update_available: x.newer })),
  ];
  const tiles = el("div.stat-row.ood-stats",
    reportStat(o.imagesKnown ? String(o.oldCount) : "–", `images over ${o.oldDays} days old`, o.imagesKnown ? `${bytes(o.oldSize)}; base images that old miss months of security fixes` : "", o.oldCount ? "warn" : ""),
    reportStat(o.imagesKnown ? String(o.latestCount) : "–", "tagged :latest", ":latest moves under you; pin a version for repeatable builds", o.latestCount ? "warn" : ""),
    reportStat(o.imagesKnown ? String(o.dangling) : "–", "dangling", o.imagesKnown ? `${bytes(o.danglingSize)} in images with no tag` : "", o.dangling ? "warn" : ""),
    reportStat(o.imagesKnown ? String(o.unused) : "–", "unused images", o.imagesKnown ? `${bytes(o.unusedSize)} not used by any container` : ""),
    reportStat(o.toolsKnown ? String(o.behind) : "–", "tools behind", o.toolsKnown ? "installed version older than the latest release" : "not checked yet", o.behind ? "bad" : ""));

  const ageColors = ["var(--series-3)", "var(--series-1)", "var(--series-4)", "var(--series-2)", "var(--series-8)"];
  const images = el("div.ood-col",
    el("h3", "Images by age"),
    o.imagesKnown
      ? Charts.hbars(o.ages.map((a, i) => ({ label: a.label, sub: a.count ? bytes(a.size) : "", value: a.count, color: ageColors[i], text: String(a.count) })))
      : el("p.empty.small", o.imagesError ? `Docker did not answer: ${o.imagesError}` : "Images are listed while the profile runs."),
    o.old.length ? [el("h3", `Older than ${o.oldDays} days`), el("ul.ood-list", o.old.map((i) => el("li",
      el("strong.mono", i.ref), el("span.muted.small", `${age(i.ageDays)} · ${bytes(i.size)}${i.containers ? ` · ${i.containers} containers` : " · unused"}`))))] : null,
    o.latest.length ? [el("h3", "Tagged :latest"), el("ul.ood-list", o.latest.map((i) => el("li",
      el("strong.mono", i.ref), el("span.muted.small", `built ${age(i.ageDays)} ago · ${bytes(i.size)}`))))] : null,
    el("div.ood-acts",
      el("button.mini.ghost", { type: "button", onclick: () => { location.hash = "#images"; } }, "Open Images"),
      o.latestCount ? el("button.mini.ghost", { type: "button", onclick: () => { location.hash = "#images?q=*:latest"; } }, "Show :latest images") : null));

  const tools = el("div.ood-col",
    el("h3", "Tools"),
    o.toolsKnown
      ? el("ul.ood-tools", o.tools.map((x) => el("li",
        el(`span.pill.${x.newer ? "warn" : x.latest ? "ok" : "x"}`, x.newer ? "update" : x.latest ? "latest" : "info"),
        el("span.ood-tool", el("strong", x.label), x.about ? el("span.muted.small", ` ${x.about}`) : null),
        el("span.mono.small.ood-ver", x.newer ? `${x.current} → ${x.latest}` : x.current))))
      : el("p.empty.small", "Versions have not been checked yet: open General, where the Updates list checks them."),
    el("div.ood-acts", el("button.mini", { type: "button", onclick: goToUpdates }, o.behind ? `Go to updates (${o.behind})` : "Go to updates")));

  return vizCard("Out of date", "old images and tools behind their latest release", "report-out-of-date", rows,
    el("div.report-body", tiles, el("div.ood-grid", images, tools)));
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
