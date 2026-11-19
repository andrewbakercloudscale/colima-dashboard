"use strict";
// Security: CVE scan with Trivy of every image the containers use (or every image), with a
// grade per image, charts of what was found and a drawer listing each CVE and what to do.

const SEVS = ["CRITICAL", "HIGH", "MEDIUM", "LOW", "UNKNOWN"];
const SEV_NAME = { CRITICAL: "Critical", HIGH: "High", MEDIUM: "Medium", LOW: "Low", UNKNOWN: "Unknown" };
const SEV_COLOR = { CRITICAL: "var(--sev-crit)", HIGH: "var(--sev-high)", MEDIUM: "var(--sev-med)", LOW: "var(--sev-low)", UNKNOWN: "var(--sev-unk)" };
const VULN_GRADE_WORDS = { A: "Nothing serious", B: "Nothing urgent", C: "High issues with a fix", D: "Critical issues", F: "Fix now" };

function sevPill(sev, n) {
  return el(`span.sev-pill.s-${sev.toLowerCase()}`, n === undefined ? SEV_NAME[sev] : `${n} ${SEV_NAME[sev]}`);
}
// An untagged image shows as its short id rather than a 71 character digest.
function vulnName(ref) { return /^sha256:[a-f0-9]{64}$/.test(ref) ? `untagged ${short(ref)}` : ref; }
function sumOf(m) { return Object.values(m || {}).reduce((a, b) => a + b, 0); }

async function installTrivy(after) {
  runJob("POST", "/api/install", { formula: "trivy" }, after);
}

async function startVulnScan(body, after) {
  try {
    const r = await api("POST", `${P()}/vulns/scan`, body);
    toast(r.total ? `Working out the cyber risk of ${r.total} image${r.total === 1 ? "" : "s"}` : "Every image already has a rating. Use Recalculate Cyber Risk to work it out again.");
    after?.();
  } catch (e) { toast(e.message, true); }
}

// A grade button for an image's cyber risk, or a muted note when it has no rating yet.
function cyberBadge(summary, id, label) {
  if (!summary) return el("span.muted.small", "not rated");
  const worst = (summary.worstComponents || []).map((c) => `${c.name} ${c.version} (${c.grade})`).join(", ");
  return el("button.bl-gbtn", { type: "button", title: `${VULN_GRADE_WORDS[summary.grade] || ""}: ${summary.total} CVEs in ${summary.components} components${worst ? `. Worst: ${worst}` : ""}. Click for details.`,
    onclick: () => showVulns(id, label) }, gradeBadge(summary.grade),
  summary.total ? el("span.small.muted", `${summary.total} CVEs`) : el("span.small.muted", "clean"));
}

// The few riskiest components of an image, as small graded chips.
function worstComponentChips(summary) {
  const list = summary?.worstComponents || [];
  return list.length ? el("div.vs-comps", list.map((c) => el(`span.vs-comp.g-${c.grade.toLowerCase()}`, { title: `${c.total} CVEs, ${c.fixable} with a fix${c.upgradeTo ? `; upgrade to ${c.upgradeTo}` : ""}` },
    el("b", c.grade), ` ${c.name}`))) : null;
}

async function renderSecurity(main) {
  const all = safeGet("vulnall") === "1";
  const scope = el("div.seg",
    el(`button.${all ? "off" : "on"}`, { type: "button", onclick: () => { safeSet("vulnall", "0"); render(); } }, "Containers' images"),
    el(`button.${all ? "on" : "off"}`, { type: "button", onclick: () => { safeSet("vulnall", "1"); render(); } }, "All images"));
  const recalc = el("button.solid", { type: "button", "data-act": "go", "data-act-fixed": "" }, "Recalculate Cyber Risk");
  const head = el("div.head",
    el("div", el("h1", "Security"), el("p.muted", "Cyber risk of the images your containers run: known vulnerabilities (CVEs) in every component, found with Trivy. Ratings are kept until you recalculate them.")),
    el("div.toolbar", scope, recalc));
  if (!isRunning()) { main.replaceChildren(head, notRunningCard()); return; }
  const progress = el("div.vs-progress", { hidden: true });
  const overview = el("div");
  const table = el("div.card.info-card", cardHead("t1", "box", "Images"), el("p.muted", "Loading…"));
  main.replaceChildren(head, progress, overview, table);

  let last = "";
  const load = async () => {
    let d;
    try { d = await api("GET", `${P()}/vulns${all ? "?all=1" : ""}`); } catch (e) { table.replaceChildren(el("div.notice.bad", e.message)); return; }
    const sig = JSON.stringify(d);
    if (sig === last) return;
    last = sig;
    draw(d);
  };
  recalc.addEventListener("click", () => startVulnScan({ all, force: true }, load));

  const draw = (d) => {
    recalc.disabled = !d.trivy || d.scan.running;
    if (!d.trivy) {
      progress.hidden = true;
      overview.replaceChildren(el("div.card.info-card.vs-install", cardHead("t8", "shield", "Trivy is needed"),
        el("div.vs-install-body",
          el("p", "The scan uses Trivy, a free open source scanner from Aqua Security. It runs on your Mac, reads the images through Colima's Docker socket and checks every OS package and app dependency against its vulnerability database."),
          el("p.muted.small", "Installs with Homebrew (brew install trivy). The first scan downloads its database, about 80 MB."),
          el("button.solid", { type: "button", "data-act": "go", "data-act-fixed": "", onclick: () => installTrivy(() => { last = ""; load(); }) }, "Install Trivy"))));
      table.hidden = true;
      return;
    }
    table.hidden = false;
    drawProgress(d.scan);
    const rows = d.images;
    const done = rows.filter((r) => r.summary);
    if (!rows.length) {
      overview.replaceChildren();
      table.replaceChildren(cardHead("t1", "box", "Images"), el("p.empty", all ? "No images in this profile." : "No containers yet, so there is nothing to scan. Switch to All images to scan every image."));
      return;
    }
    overview.replaceChildren(done.length ? overviewCard(done) : el("div.card.vs-first",
      el("p", el("strong", `${rows.length} image${rows.length === 1 ? "" : "s"} not rated yet.`), " Each takes a few seconds; the first run also downloads Trivy's database. The ratings are kept until you recalculate them."),
      d.scan.running ? null : el("button.solid", { type: "button", "data-act": "go", "data-act-fixed": "", onclick: () => startVulnScan({ all }, load) }, "Calculate Cyber Risk")));
    table.replaceChildren(cardHead("t1", "box", "Images", `${done.length} of ${rows.length} rated`), imageTable(rows, d.scan));
  };

  const drawProgress = (sc) => {
    const failed = sc.failed || [];
    if (!sc.running && !sc.error && !failed.length) { progress.hidden = true; return; }
    progress.hidden = false;
    const fill = el("i");
    fill.style.setProperty("width", `${sc.total ? Math.round((sc.done / sc.total) * 100) : 4}%`);
    progress.replaceChildren(
      sc.running ? el("div.vs-prog-line", el("span.spin"),
        el("span", sc.phase === "db" ? "Updating Trivy's vulnerability database…" : `Rating ${vulnName(sc.current || "…")} (${sc.done + 1} of ${sc.total})`),
        el("div.bl-bar", fill)) : null,
      sc.error ? el("div.notice.bad", sc.error) : null,
      !sc.running && failed.length ? el("details.vs-failed", el("summary", `${failed.length} image${failed.length === 1 ? "" : "s"} could not be scanned`),
        el("ul", failed.map((f) => el("li.mono.small", f)))) : null);
  };

  const imageTable = (rows, sc) => {
    const tb = el("tbody");
    for (const r of rows) {
      const s = r.summary;
      const scanningThis = sc.running && sc.current === r.ref;
      const counts = s ? el("div", el("div.vs-counts", SEVS.slice(0, 4).map((k) => s.counts?.[k] ? sevPill(k, s.counts[k]) : null),
        s.total ? null : el("span.vs-clean", "No known CVEs")), worstComponentChips(s)) : el("span.muted", scanningThis ? "Rating…" : "Not rated yet");
      tb.append(el("tr",
        el("td", s ? el("button.bl-gbtn", { type: "button", title: VULN_GRADE_WORDS[s.grade] || "", onclick: () => showVulns(r.id, vulnName(r.ref)) }, gradeBadge(s.grade)) : el("span.bl-grade.g-x", "?")),
        el("td", el("div.vs-img", el("strong.mono", { title: r.ref }, vulnName(r.ref)),
          el("span.muted.small", s?.os || "", s?.eol ? el("span.pill.vs-eol", "OS end of life") : null))),
        el("td", r.containers?.length
          ? el("div.vs-ctrs", r.containers.slice(0, 4).map((c) => el(`span.vs-ctr${c.running ? ".on" : ""}`, c.name)),
            r.containers.length > 4 ? el("span.muted.small", `+${r.containers.length - 4}`) : null)
          : el("span.muted.small", "no container")),
        el("td", counts),
        el("td.num", s ? String(sumOf(s.fixable)) : "–"),
        el("td.muted.small", r.scannedAt ? ago(new Date(r.scannedAt).getTime() / 1000) : "–"),
        el("td.vs-actions",
          s ? el("button.ghost.mini", { type: "button", onclick: () => showVulns(r.id, vulnName(r.ref)) }, "Details") : null,
          el("button.ghost.mini", { type: "button", disabled: sc.running, onclick: () => startVulnScan({ ids: [r.id] }, load) }, s ? "Recalculate" : "Rate"))));
    }
    return el("div.table-wrap", el("table.vs-table",
      el("thead", el("tr", ["Cyber risk", "Image", "Used by", "Vulnerabilities and riskiest components", "Fixable", "Rated", ""].map((h) => el("th", h)))), tb));
  };

  setTimer(load, 2000, main);
  await load();
}

// The charts above the table: totals by severity, then the worst images stacked by severity.
function overviewCard(done) {
  const totals = Object.fromEntries(SEVS.map((k) => [k, done.reduce((a, r) => a + (r.summary.counts?.[k] || 0), 0)]));
  const fixable = done.reduce((a, r) => a + sumOf(r.summary.fixable), 0);
  const all = sumOf(totals);
  const atRisk = done.filter((r) => ["D", "F"].includes(r.summary.grade)).length;
  const eol = done.filter((r) => r.summary.eol).length;
  const tile = (hue, ic, label, value, tone) => el(`div.tile.${hue}${tone ? "." + tone : ""}`, chip(ic),
    el("div.tile-text", el("span.tile-label", label), el("span.tile-value", value)));
  const donut = Charts.donut(SEVS.map((k) => ({ label: SEV_NAME[k], value: totals[k], color: SEV_COLOR[k] })), 168, [String(all), all === 1 ? "CVE" : "CVEs"]);
  const legend = el("ul.vs-legend", SEVS.filter((k) => totals[k]).map((k) => {
    const dot = el("i");
    dot.style.setProperty("background", SEV_COLOR[k]);
    return el("li", dot, el("span", SEV_NAME[k]), el("strong", String(totals[k])));
  }));
  const worst = [...done].sort((a, b) => sevScore(b.summary) - sevScore(a.summary)).filter((r) => r.summary.total).slice(0, 10);
  const bars = worst.length ? Charts.hbars(worst.map((r) => ({
    label: vulnName(r.ref), sub: r.summary.top || "",
    parts: SEVS.map((k) => ({ name: SEV_NAME[k], value: r.summary.counts?.[k] || 0, color: SEV_COLOR[k] })),
    text: `${r.summary.total}`,
  }))) : el("p.bl-allgood", "No known vulnerabilities in any scanned image.");
  return el("div.vs-overview",
    el("div.tiles.vs-tiles",
      tile("t8", "shield", "Critical", String(totals.CRITICAL), totals.CRITICAL ? "bad" : "ok"),
      tile("t2", "shield", "High", String(totals.HIGH), totals.HIGH ? "bad" : "ok"),
      tile("t4", "shield", "Medium", String(totals.MEDIUM)),
      tile("t3", "wrench", "Have a fix", String(fixable), fixable ? "" : "ok"),
      tile("t5", "box", "Images at risk (D or F)", `${atRisk} of ${done.length}`, atRisk ? "bad" : "ok"),
      eol ? tile("t7", "clock", "OS end of life", String(eol), "bad") : null),
    el("div.vs-charts",
      el("div.card.info-card", cardHead("t8", "shield", "By severity"), el("div.vs-donut", donut, legend)),
      el("div.card.info-card", cardHead("t2", "layers", "Worst images", "by severity, most serious first"), bars)));
}

function sevScore(s) {
  const c = s.counts || {};
  return (c.CRITICAL || 0) * 1e6 + (c.HIGH || 0) * 1e3 + (c.MEDIUM || 0) + (s.eol ? 1e9 : 0);
}

// The drawer: grade, what to do, then every CVE with filters.
async function showVulns(id, ref) {
  openDrawer(`CVEs: ${ref}`, el("p.muted", "Loading…"));
  const token = {};
  state.drawerToken = token;
  let d;
  try { d = await api("GET", `${P()}/vulns/${encodeURIComponent(id)}`); } catch (e) {
    if (state.drawerToken === token) $("#drawerbody").replaceChildren(el("div.notice.bad", e.message));
    return;
  }
  if (state.drawerToken !== token) return;
  const r = d.report;
  const vulns = r.vulns || [];
  const fixable = sumOf(r.fixable);
  const tile = (hue, ic, label, value, tone) => el(`div.tile.${hue}${tone ? "." + tone : ""}`, chip(ic),
    el("div.tile-text", el("span.tile-label", label), el("span.tile-value", value)));
  const hero = el("div.bl-hero",
    el("div.bl-score", gradeBadge(r.grade, { big: true }), el("div.bl-score-text",
      el("strong", `${r.total} known vulnerabilit${r.total === 1 ? "y" : "ies"}`),
      el("span.bl-verdict", VULN_GRADE_WORDS[r.grade] || ""),
      el("span.muted.small", `${r.os ? r.os + " · " : ""}rated ${ago(new Date(d.scannedAt).getTime() / 1000)} with Trivy`),
      el("button.ghost.mini.vs-recalc", { type: "button", onclick: (e) => recalcImage(e.target, id, ref, token) }, "Recalculate Cyber Risk"))),
    el("div.tiles.bl-hero-tiles",
      SEVS.slice(0, 4).map((k) => {
        const t = tile({ CRITICAL: "t8", HIGH: "t2", MEDIUM: "t4", LOW: "t1" }[k], "shield", SEV_NAME[k], String(r.counts?.[k] || 0), r.counts?.[k] && k !== "LOW" && k !== "MEDIUM" ? "bad" : "");
        return t;
      }),
      tile("t3", "wrench", "Have a fix", String(fixable))));
  const advice = r.advice || [];
  const fixCard = el("div.card.fix-card.bl-fixes",
    el("div.fix-head", el("h2", "What to do"), el("span.muted.small", advice.length ? "most important first" : "")),
    advice.length ? el("ul.fix-list", advice.map((a) => el(`li.fix-item.${a.severity === "high" ? "fail" : a.severity === "low" ? "info" : "warn"}`,
      el("div.fix-title", el(`span.sev${a.severity === "high" ? ".fail" : a.severity === "low" ? ".info" : ""}`, a.severity), el("strong", a.title)),
      el("p.fix-detail", a.detail),
      a.fix ? el("div.bl-code", el("pre.code-block", a.fix), copyBtn(a.fix)) : null)))
      : el("p.bl-allgood", "Nothing to do: no known vulnerabilities."));

  // Every component with findings, graded on its own, worst first.
  const comps = r.components || [];
  let compLimit = 25;
  const compBody = el("div");
  const drawComps = () => {
    compBody.replaceChildren(el("div.table-wrap", el("table.vs-cves.vs-comp-table",
      el("thead", el("tr", ["Cyber risk", "Component", "Version", "Vulnerabilities", "Fixable", "Upgrade to"].map((h) => el("th", h)))),
      el("tbody", comps.slice(0, compLimit).map((c) => el("tr",
        el("td", el("button.bl-gbtn", { type: "button", title: `Show ${c.name}'s CVEs`, onclick: () => { q.value = c.name; limit = 200; drawList(); listCard.scrollIntoView({ behavior: "smooth", block: "start" }); } }, gradeBadge(c.grade))),
        el("td", el("strong.mono", c.name), el("span.vs-target", c.kind === "os" ? "OS package" : c.target)),
        el("td.mono.small", c.version),
        el("td", el("div.vs-counts", SEVS.slice(0, 4).map((k) => c.counts?.[k] ? sevPill(k, c.counts[k]) : null))),
        el("td.num", String(c.fixable)),
        el("td.mono.small", c.upgradeTo ? el("span.vs-fixed", c.upgradeTo) : el("span.muted", "no fix yet"))))))),
    comps.length > compLimit ? el("button.ghost.mini.vs-more", { type: "button", onclick: () => { compLimit += 50; drawComps(); } }, `Show more (${comps.length - compLimit} left)`) : "");
  };
  const gradeCount = (g) => comps.filter((c) => c.grade === g).length;
  const compCard = comps.length ? el("div.card.info-card", cardHead("t2", "layers", "Components", `${comps.length} with findings · ${["F", "D", "C", "B", "A"].map((g) => gradeCount(g) ? `${gradeCount(g)} ${g}` : "").filter(Boolean).join(", ")}`),
    el("p.small.muted.vs-comp-note", "Each package graded on its own: F has a critical CVE with a fix, D a critical with none, C a high with a fix, B a high with none or a medium, A only low. Click a grade to see its CVEs."),
    compBody) : null;
  if (compCard) drawComps();

  // The list, with severity toggles, "only with a fix" and a search box.
  const on = new Set(SEVS);
  let onlyFix = false;
  const q = el("input", { type: "search", placeholder: "Search CVE, package or title", "aria-label": "Search vulnerabilities" });
  const fixBox = el("input", { type: "checkbox", id: "vs-onlyfix" });
  const chips = el("div.vs-chips", SEVS.filter((k) => r.counts?.[k]).map((k) => {
    const b = el(`button.mini.vs-chip.s-${k.toLowerCase()}.on`, { type: "button", "aria-pressed": "true" }, `${SEV_NAME[k]} ${r.counts[k]}`);
    b.addEventListener("click", () => {
      if (on.has(k)) on.delete(k); else on.add(k);
      b.classList.toggle("on", on.has(k));
      b.setAttribute("aria-pressed", String(on.has(k)));
      drawList();
    });
    return b;
  }));
  const list = el("div.vs-list");
  let limit = 200;
  const drawList = () => {
    const term = q.value.trim().toLowerCase();
    const rows = vulns.filter((v) => on.has(v.severity) && (!onlyFix || v.fixed) &&
      (!term || `${v.id} ${v.pkg} ${v.title || ""} ${v.target}`.toLowerCase().includes(term)));
    if (!rows.length) { list.replaceChildren(el("p.empty", vulns.length ? "Nothing matches." : "No known vulnerabilities.")); return; }
    list.replaceChildren(el("table.vs-cves",
      el("thead", el("tr", ["Severity", "CVE", "Package", "Installed", "Fixed in", "What it is"].map((h) => el("th", h)))),
      el("tbody", rows.slice(0, limit).map((v) => el("tr",
        el("td", sevPill(v.severity)),
        el("td.mono", v.url ? el("a", { href: v.url, target: "_blank", rel: "noopener noreferrer" }, v.id) : v.id),
        el("td", el("strong.mono", v.pkg), el("span.vs-target", v.kind === "os" ? "OS package" : v.target)),
        el("td.mono.small", v.installed),
        el("td.mono.small", v.fixed ? el("span.vs-fixed", v.fixed) : el("span.muted", v.status === "will_not_fix" ? "won't fix" : "no fix yet")),
        el("td.small", v.title || "–"))))),
    rows.length <= limit ? "" : el("button.ghost.mini.vs-more", { type: "button", onclick: () => { limit += 300; drawList(); } }, `Show more (${rows.length - limit} left)`));
  };
  q.addEventListener("input", () => { limit = 200; drawList(); });
  fixBox.addEventListener("change", () => { onlyFix = fixBox.checked; limit = 200; drawList(); });
  const listCard = el("div.card.info-card", cardHead("t1", "shield", "Every vulnerability", `${vulns.length} found`),
    el("div.vs-filters", chips, el("label.vs-onlyfix", fixBox, el("span", "Only ones with a fix")), q), list);
  drawList();
  $("#drawerbody").replaceChildren(...[hero, fixCard, compCard, listCard].filter(Boolean));
}

// Rates one image again, then redraws its drawer if it is still the one open.
async function recalcImage(btn, id, ref, token) {
  btn.disabled = true;
  btn.textContent = "Recalculating…";
  try { await api("POST", `${P()}/vulns/scan`, { ids: [id] }); } catch (e) { toast(e.message, true); btn.disabled = false; btn.textContent = "Recalculate Cyber Risk"; return; }
  for (let i = 0; i < 150; i++) {
    await new Promise((r) => setTimeout(r, 2000));
    if (state.drawerToken !== token) return;
    let d;
    try { d = await api("GET", `${P()}/vulns?all=1`); } catch { continue; }
    if (!d.scan.running) {
      if (d.scan.failed?.length) toast(d.scan.failed[0], true); else toast("Cyber risk recalculated");
      showVulns(id, ref);
      return;
    }
  }
}

tabs.security = renderSecurity;
