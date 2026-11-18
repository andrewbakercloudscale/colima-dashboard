// Errors: error and warning lines from every container's and every pod's logs in one place,
// newest first, with a chart of when they happened and who logged them. The sidebar badge and
// General's things to fix count the last 15 minutes, so new errors show without looking here.
"use strict";

const ERR_RANGES = { 900: "15 min", 3600: "1 hour", 21600: "6 hours", 86400: "24 hours" };

function errRange() {
  const r = Number(safeGet("errRange"));
  return ERR_RANGES[r] ? r : 3600;
}

async function loadErrors(since) {
  return api("GET", `${P()}/errors?since=${since}`);
}

// The sidebar badge: errors in the last 15 minutes.
async function refreshErrBadge() {
  const b = $("#errbadge");
  if (!b) return;
  if (!isRunning()) { b.hidden = true; return; }
  try {
    const d = await loadErrors(900);
    b.hidden = d.errors === 0 && d.warnings === 0;
    b.textContent = String(d.errors || d.warnings);
    b.className = d.errors ? "badge" : "badge warn";
    b.title = `${d.errors} errors, ${d.warnings} warnings in the last 15 minutes`;
  } catch { /* badge stays as it was */ }
}

// errorsRow is General's "things to fix" entry for recent errors, or null.
function errorsRow(d) {
  if (!d || !d.errors) return null;
  const top = d.sources.filter((s) => s.errors).slice(0, 3).map((s) => `${s.name} (${s.errors})`);
  return el("li.fix-item.fail",
    el("div.fix-title", el("strong", `${d.errors} ${d.errors === 1 ? "error" : "errors"} in logs, last 15 minutes`), el("span.sev.fail", "Problem")),
    el("p.fix-detail", `From ${top.join(", ")}${d.sources.filter((s) => s.errors).length > 3 ? " and more" : ""}.`),
    el("div.copy", el("button.mini", { type: "button", onclick: () => { location.hash = "#errors"; } }, "Show errors")));
}

const errState = { source: "", level: "", q: "" };

// goToSource opens the container on the Containers page (filtered to it, details open), or a
// pod's logs; openLine opens its log on the very line.
function goToSource(s) {
  if (s.kind === "pod") return showPodLogs({ namespace: s.namespace, name: s.name });
  palGo("containers", { q: s.name, name: s.name, open: "inspect" });
}
function openLine(s, ts) {
  if (!s) return;
  if (s.kind === "pod") return showPodLogs({ namespace: s.namespace, name: s.name });
  showLogs(s.id, s.name, ts);
}
const srcLink = (s, label) => el("button.link.err-go", { type: "button", title: s.kind === "pod" ? "Open this pod's logs" : "Go to this container",
  onclick: (e) => { e.stopPropagation(); goToSource(s); } }, label || s.name);

async function renderErrors(main) {
  const since = errRange();
  const seg = el("div.seg", Object.entries(ERR_RANGES).map(([r, label]) =>
    el(`button.${Number(r) === since ? "on" : "off"}`, { type: "button", onclick: () => { safeSet("errRange", r); render(); } }, label)));
  const head = el("div.head",
    el("div", el("h1", "Errors"), el("p.muted", "Error and warning lines from every container's and every pod's logs, newest first. Click a line to open its log on that line, or a name to go to the container.")), seg);
  if (!isRunning()) { main.replaceChildren(head, notRunningCard()); return; }
  const body = el("div", el("div.card", el("p.muted", "Reading logs…")));
  main.replaceChildren(head, body);

  const paint = (d) => {
    const srcBy = Object.fromEntries(d.sources.map((s) => [s.key, s]));
    const tiles = el("div.err-tiles",
      tile("Errors", d.errors, d.errors ? "bad" : "ok"),
      tile("Warnings", d.warnings, d.warnings ? "warn" : "ok"),
      tile("Sources with errors", `${d.sources.filter((s) => s.errors).length} of ${d.scanned}`, d.sources.some((s) => s.errors) ? "bad" : "ok", "containers and pods"));

    // When: errors and warnings per bucket, 30 buckets over the range.
    const n = 30, width = (since * 1000) / n, start = d.at - since * 1000;
    const times = Array.from({ length: n }, (_, i) => start + i * width);
    const errs = new Array(n).fill(0), warns = new Array(n).fill(0);
    for (const l of d.lines) {
      const t = Date.parse(l.ts);
      if (isNaN(t)) continue;
      const i = Math.min(n - 1, Math.max(0, Math.floor((t - start) / width)));
      (l.level === "error" ? errs : warns)[i]++;
    }
    const chart = el("section.card.viz",
      el("div.viz-head", el("h2", "When"), el("span.small.muted", d.truncated ? `newest ${d.lines.length} lines` : "")),
      barChart({ times, width, series: [
        { name: "Errors", color: "var(--bad)", values: errs },
        { name: "Warnings", color: "var(--warn)", values: warns },
      ], empty: "No errors or warnings in this time." }));

    // Who: one row per container or pod with errors or warnings.
    const who = el("section.card",
      el("h2", "Who"),
      d.sources.length ? el("div.err-who", el("table.ctable", el("thead", el("tr", el("th", "Source"), el("th", "Errors"), el("th", "Warnings"), el("th", "Last"), el("th", ""))),
        el("tbody", d.sources.map((s) => el(`tr${errState.source === s.key ? ".on" : ""}`,
          el("td", el(`span.pill.err-kind.${s.kind}`, s.kind), " ", srcLink(s),
            s.project ? el("span.small.muted", ` ${s.project}`) : s.namespace ? el("span.small.muted", ` ${s.namespace}`) : null,
            s.state && s.state !== "running" ? el("span.small.muted", ` (${s.state})`) : null),
          el("td", s.errors ? el("strong.err-n", String(s.errors)) : "0"),
          el("td", String(s.warnings)),
          el("td.nowrap", s.last ? new Date(s.last).toLocaleTimeString(undefined, { hour12: false }) : ""),
          el("td.actions",
            el("button.ghost.mini", { type: "button", onclick: () => { errState.source = errState.source === s.key ? "" : s.key; paint(d); } }, errState.source === s.key ? "Show all" : "Only this"),
            s.kind === "pod" ? null : el("button.mini", { type: "button", onclick: () => goToSource(s) }, "Open container"),
            el("button.ghost.mini", { type: "button", onclick: () => openLine(s, s.last) }, "Full log")))))))
        : el("p.empty", `Nothing logged an error or a warning. ${d.scanned} containers and pods read.`),
      d.podError ? el("p.small.muted", "Kubernetes pods: ", d.podError) : null);

    // The lines themselves.
    const q = el("input", { type: "search", placeholder: "Filter lines", value: errState.q, "aria-label": "Filter lines" });
    const lvl = el("div.seg", [["", "All"], ["error", "Errors"], ["warn", "Warnings"]].map(([v, label]) =>
      el(`button.${errState.level === v ? "on" : "off"}`, { type: "button", onclick: () => { errState.level = v; paint(d); } }, label)));
    const box = el("div.log.err-log");
    const fill = () => {
      const needle = errState.q.toLowerCase();
      const rows = d.lines.filter((l) => (!errState.source || l.source === errState.source) && (!errState.level || l.level === errState.level) &&
        (!needle || l.text.toLowerCase().includes(needle) || (srcBy[l.source]?.name || "").toLowerCase().includes(needle))).slice(0, 600);
      box.replaceChildren(...(rows.length ? rows.map((l) => {
        const s = srcBy[l.source];
        const line = logfmt.line(l.text, errState.q, l.level === "error");
        line.prepend(el("span.lg-ts", { title: l.ts }, l.ts ? new Date(l.ts).toLocaleTimeString(undefined, { hour12: false }) : ""), " ",
          s ? srcLink(s) : el("span.err-src", l.source), " ");
        line.classList.add("err-line");
        line.title = s?.kind === "pod" ? "Click to open this pod's logs" : "Click to open the log on this line";
        line.addEventListener("click", () => { if (!getSelection().toString()) openLine(s, l.ts); });
        return line;
      }) : [el("p.muted", "No lines match.")]));
    };
    q.addEventListener("input", () => { errState.q = q.value; fill(); });
    fill();
    const lines = el("section.card",
      el("div.viz-head", el("h2", "Lines"), el("div.err-tools", lvl, q)), box);
    body.replaceChildren(tiles, chart, who, lines);
  };

  const load = async () => {
    try {
      const d = await loadErrors(since);
      if (state.tab !== "errors") return;
      if (errState.source && !d.sources.some((s) => s.key === errState.source)) errState.source = "";
      paint(d);
    } catch (e) {
      body.replaceChildren(el("div.notice.bad", el("p", e.message)));
    }
  };
  await load();
  setTimer(load, 15000);
}

function tile(label, value, tone, sub) {
  return el(`div.err-tile.${tone}`, el("span.tile-label", label), el("span.err-tile-v", String(value)), sub ? el("span.small.muted", sub) : null);
}

tabs.errors = renderErrors;
