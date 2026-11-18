// Image bloat: a grade for every image on the Images page and a drawer that shows where the
// space goes (layers, folders, biggest files), what to fix and a suggested multi-stage Dockerfile.
// Loaded before app.js; it only uses app.js helpers (el, api, P, bytes, short, toast, openDrawer,
// chip, cardHead, copyBtn, state) when called.
"use strict";

const GRADE_HUE = { A: "g-a", B: "g-b", C: "g-c", D: "g-d", F: "g-f" };
const KIND_COLOR = { RUN: "var(--series-1)", COPY: "var(--series-3)", ADD: "var(--series-4)" };
const STACK_NAME = { node: "Node.js", python: "Python", go: "Go", java: "Java", rust: "Rust", dotnet: ".NET", ruby: "Ruby", php: "PHP" };

function gradeBadge(grade, opts = {}) {
  return el(`span.bl-grade.${GRADE_HUE[grade] || "g-x"}${opts.big ? ".big" : ""}`, { title: opts.title || "" }, grade || "?");
}

// loadBloatSummary fetches the quick check for every image (cached on the server per image ID).
async function loadBloatSummary() {
  return api("GET", `${P()}/images/bloat`);
}

// bloatSummaryCard is the card above the image table. It shows the quick check at once, then
// scans each image's files one by one (the server keeps every answer, so this is slow only the
// first time an image is seen). onRows hands the rows back each time so the table can show grades.
function bloatSummaryCard(onRows) {
  const body = el("div.bl-sum-body", el("p.muted", "Checking images for wasted space…"));
  const note = el("span.muted.small", "Checks build history, then scans each image's files.");
  const card = el("div.card.info-card.bl-sum", cardHead("t8", "layers", "Image bloat"), body);
  card.querySelector(".info-head").append(note);
  let rows = [];
  const tile = (hue, ic, label, value, tone) => el(`div.tile.${hue}${tone ? "." + tone : ""}`, chip(ic),
    el("div.tile-text", el("span.tile-label", label), el("span.tile-value", value)));
  const paint = (done, total) => {
    const ok = rows.filter((r) => r.score >= 0).sort((a, b) => a.score - b.score);
    if (!ok.length) { body.replaceChildren(el("p.empty", "No images to check.")); return; }
    const save = ok.reduce((a, r) => a + (r.saving || 0), 0);
    const bad = ok.filter((r) => r.grade === "D" || r.grade === "F").length;
    const lean = ok.filter((r) => r.grade === "A").length;
    const scanning = done < total;
    note.textContent = scanning ? `Scanning files: ${done} of ${total} images` : "Build history and files checked. Click a grade for details.";
    const worst = ok.filter((r) => r.grade !== "A").slice(0, 5);
    const fill = el("i");
    fill.style.setProperty("width", `${Math.round((done / total) * 100)}%`);
    body.replaceChildren(
      scanning ? el("div.bl-progress", el("span.spin"), el("div.bl-bar", fill)) : "",
      el("div.tiles.bl-tiles",
        tile("t1", "layers", "Images checked", String(ok.length)),
        tile("t3", "box", "Lean (grade A)", String(lean), lean === ok.length ? "ok" : ""),
        tile("t8", "wrench", "Need work (D or F)", String(bad), bad ? "bad" : "ok"),
        tile("t4", "disk", "Could shed", save ? `~${bytes(save)}` : "–")),
      worst.length
        ? el("ul.bl-worst", worst.map((r) => el("li",
          gradeBadge(r.grade),
          el("div.bl-worst-main", el("strong.mono", r.ref),
            el("span.muted.small", [r.saving ? `could save ~${bytes(r.saving)}` : "", r.top, r.issues > 1 ? `${r.issues} findings` : ""].filter(Boolean).join(" · "))),
          el("button.ghost.mini", { type: "button", onclick: () => showBloat(r.id, r.ref) }, "Inspect"))))
        : el("p.bl-allgood", scanning ? "Nothing flagged so far." : "Every image looks lean. Nice."));
  };
  loadBloatSummary().then(async (got) => {
    rows = got;
    const todo = rows.filter((r) => r.score >= 0 && !r.deep);
    const total = rows.filter((r) => r.score >= 0).length;
    let done = total - todo.length;
    onRows(rows); paint(done, total);
    for (const r of todo) {
      if (!card.isConnected) return;
      try {
        const rep = await api("GET", `${P()}/images/${encodeURIComponent(r.id)}/bloat?deep=1`);
        const top = (rep.findings || []).find((f) => f.severity !== "low");
        Object.assign(r, { score: rep.score, grade: rep.grade, saving: rep.saving, deep: true, top: top ? top.title : "", issues: (rep.findings || []).length });
      } catch { /* keep the quick answer */ }
      done++;
      if (!card.isConnected) return;
      onRows(rows); paint(done, total);
    }
  }).catch((e) => body.replaceChildren(el("p.muted", `Bloat check failed: ${e.message}`)));
  return card;
}

async function showBloat(id, name) {
  openDrawer(`Bloat: ${name}`, el("p.muted", "Reading the build history…"));
  const token = {};
  state.bloatToken = token;
  const url = `${P()}/images/${encodeURIComponent(id)}/bloat`;
  try {
    const quick = await api("GET", url);
    if (state.bloatToken !== token) return;
    $("#drawerbody").replaceChildren(...bloatView(quick, true));
    const deep = await api("GET", url + "?deep=1");
    if (state.bloatToken !== token || $("#drawer").hidden) return;
    $("#drawerbody").replaceChildren(...bloatView(deep, false));
  } catch (e) {
    if (state.bloatToken !== token) return;
    const note = el("p.bl-err", `Could not finish the scan: ${e.message}`);
    const body = $("#drawerbody");
    body.querySelector(".bl-scanning")?.remove();
    body.prepend(note);
  }
}

function bloatView(r, scanning) {
  const parts = [];
  const tile = (hue, ic, label, value, tone) => el(`div.tile.${hue}${tone ? "." + tone : ""}`, chip(ic),
    el("div.tile-text", el("span.tile-label", label), el("span.tile-value", value)));
  const pct = r.size ? Math.round((r.saving / r.size) * 100) : 0;
  parts.push(el("div.bl-hero",
    el("div.bl-score", gradeBadge(r.grade, { big: true }), el("div.bl-score-text", el("strong", `${r.score} / 100`),
      el("span.bl-verdict", r.grade === "A" ? "Lean" : r.grade === "B" ? "Fairly lean" : r.grade === "C" ? "Some bloat" : "Bloated"),
      el("span.muted.small", r.saving > 0
        ? `Fixing the findings below could take it from ${bytes(r.size)} to about ${bytes(r.size - r.saving)}.`
        : scanning ? "Build history checked; the file scan is running." : "No big savings found."))),
    el("div.tiles.bl-hero-tiles",
      tile("t1", "disk", "Unpacked size", bytes(r.size)),
      tile(r.saving ? "t8" : "t3", "wrench", "Could save", r.saving ? `~${bytes(r.saving)} (${pct}%)` : scanning ? "scanning…" : "little", r.saving ? "bad" : "ok"),
      tile("t2", "layers", "Layers", String(r.layerCount)),
      tile("t5", "box", "Base", r.base || (scanning ? "scanning…" : "unknown")),
      tile("t7", "code", "Stack", STACK_NAME[r.stack] || "generic"))));
  if (scanning) parts.push(el("p.bl-scanning", el("span.spin"), "Scanning the image's files for caches, build tools and leftovers…"));

  const findings = r.findings || [];
  const fixCard = el("div.card.fix-card.bl-fixes",
    el("div.fix-head", el("h2", "What to fix"), el("span.muted.small", findings.length ? `${findings.length} finding${findings.length === 1 ? "" : "s"}, biggest first` : "")));
  if (!findings.length) fixCard.append(el("p.bl-allgood", "Nothing to fix: this image is lean."));
  else fixCard.append(el("ul.fix-list", findings.map((f) => el(`li.fix-item.${f.severity === "high" ? "fail" : f.severity === "low" ? "info" : "warn"}`,
    el("div.fix-title",
      el(`span.sev${f.severity === "high" ? ".fail" : f.severity === "low" ? ".info" : ""}`, f.severity),
      el("strong", f.title),
      f.saving ? el("span.pill.bl-save", `saves ~${bytes(f.saving)}`) : null),
    el("p.fix-detail", f.detail),
    f.fix ? el("div.bl-code", el("pre.code-block", f.fix), copyBtn(f.fix)) : null))));
  parts.push(fixCard);

  if (r.deep && r.dirs?.length) parts.push(bloatFolders(r));
  if (r.deep && r.bigFiles?.length) {
    parts.push(el("div.card.info-card", cardHead("t4", "file", "Largest files"),
      el("div.bl-pad", Charts.hbars(r.bigFiles.slice(0, 10).map((f) => {
        const i = f.path.lastIndexOf("/");
        return { label: f.path.slice(i + 1), sub: f.path.slice(0, i) || "/", value: f.size, text: bytes(f.size), color: "var(--series-4)" };
      })))));
  }

  const layers = (r.layers || []).filter((l) => l.size > 0);
  if (layers.length) {
    const rows = layers.map((l) => ({ label: l.step, sub: l.kind, value: l.size, text: bytes(l.size), color: KIND_COLOR[l.kind] || "var(--series-7)" }));
    parts.push(el("div.card.info-card", cardHead("t1", "layers", "Layers", `${layers.length} with files, oldest first`),
      el("div.bl-pad", Charts.hbars(rows), el("p.muted.small", "Blue is RUN, green is COPY, amber is ADD. A big layer late in the list is the first place to look."))));
  }

  const stack = STACK_NAME[r.stack];
  parts.push(el("div.card.info-card.bl-suggest", cardHead("t3", "code", "Suggested Dockerfile", stack ? `multi-stage, for ${stack}` : "multi-stage, generic"),
    el("div.bl-pad",
      el("p.muted.small", "A starting point: build in one stage, copy only the result into a slim or distroless runtime stage. Change the entry file and build command to yours."),
      el("div.bl-code", el("pre.code-block", r.dockerfile), copyBtn(r.dockerfile)),
      el("h3.bl-sub", ".dockerignore"),
      el("div.bl-code", el("pre.code-block", r.dockerignore), copyBtn(r.dockerignore)),
      el("p.muted.small", "More: ",
        el("a", { href: "https://docs.docker.com/build/building/multi-stage/", target: "_blank", rel: "noopener" }, "multi-stage builds"), ", ",
        el("a", { href: "https://docs.docker.com/build/building/best-practices/", target: "_blank", rel: "noopener" }, "Dockerfile best practices"), "."))));
  return parts;
}

// bloatFolders is a drill-down bar list: top-level folders first, a click shows what is inside.
function bloatFolders(r) {
  const box = el("div.bl-pad");
  const crumbs = el("div.bl-crumbs");
  const draw = (at) => {
    const depth = at === "/" ? 1 : at.split("/").length;
    const kids = r.dirs.filter((d) => d.depth === depth && (at === "/" || d.path.startsWith(at + "/"))).slice(0, 10);
    const segs = at === "/" ? [] : at.slice(1).split("/");
    crumbs.replaceChildren(
      el("button.linkish", { type: "button", onclick: () => draw("/") }, "/"),
      ...segs.map((s, i) => [el("span.muted", " › "), el("button.linkish", { type: "button", onclick: () => draw("/" + segs.slice(0, i + 1).join("/")) }, s)]).flat());
    if (!kids.length) { box.replaceChildren(crumbs, el("p.muted.small", "Nothing big below this folder.")); return; }
    const bars = Charts.hbars(kids.map((d) => ({
      label: d.path.slice(d.path.lastIndexOf("/") + 1), sub: d.path, value: d.size, text: bytes(d.size), color: "var(--series-7)",
    })));
    [...bars.children].forEach((row, i) => {
      if (kids[i].depth >= 3) return;
      row.classList.add("bl-click");
      row.tabIndex = 0;
      row.title = `Open ${kids[i].path}`;
      const go = () => draw(kids[i].path);
      row.addEventListener("click", go);
      row.addEventListener("keydown", (e) => { if (e.key === "Enter") go(); });
    });
    box.replaceChildren(crumbs, bars, el("p.muted.small", `${r.files.toLocaleString()} files in total. Click a folder to look inside.`));
  };
  draw("/");
  return el("div.card.info-card", cardHead("t7", "folder", "Where the space goes"), box);
}
