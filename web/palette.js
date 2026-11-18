// Command palette (Cmd+K, Ctrl+K or "/"), "g" then a letter to jump, "?" for the shortcut list.
// Uses app.js globals (el, api, state, P, render, toast, confirmBox, runJob, ...). Lists load when
// the palette opens and are never polled.
"use strict";

const PAL_JUMPS = { g: "general", m: "monitor", c: "containers", i: "images", k: "kubernetes", r: "runs", b: "builds", d: "diagnostics" };
const PAL_SHORTCUTS = [
  ["⌘K / Ctrl+K / /", "Command palette"],
  ["↑ ↓ Enter Esc", "Move, run, close in the palette"],
  ...Object.entries(PAL_JUMPS).map(([k, t]) => [`g ${k}`, `Go to ${t[0].toUpperCase()}${t.slice(1)}`]),
  ["?", "This list"],
];

// Is the key meant for something the user is typing into?
function palTyping(e) {
  const t = e.target;
  return !!(t && (t.closest?.(".xterm") || t.isContentEditable || /^(INPUT|TEXTAREA|SELECT)$/.test(t.tagName)));
}

function palGo(page, params) {
  const qs = params ? new URLSearchParams(params).toString() : "";
  const hash = `#${page}${qs ? "?" + qs : ""}`;
  if (location.hash === hash) render(); else location.hash = hash;
}

// Subsequence match with a score: consecutive runs and word starts count most, then shortness.
function palScore(q, text) {
  if (!q) return 1;
  const t = text.toLowerCase();
  let ti = 0, score = 0, run = 0;
  for (const ch of q) {
    const i = t.indexOf(ch, ti);
    if (i < 0) return 0;
    run = i === ti ? run + 1 : 1;
    score += run * 2 + (i === 0 || /[\s\-_:/.]/.test(t[i - 1]) ? 3 : 0);
    ti = i + 1;
  }
  if (t.startsWith(q)) score += 10;
  return score + 1 / (1 + t.length);
}

const pal = { open: false, items: [], shown: [], sel: 0, root: null, input: null, list: null };

function palBaseItems() {
  const items = [];
  for (const a of document.querySelectorAll("#nav a")) {
    items.push({ kind: "Page", label: a.textContent.trim(), run: () => palGo(a.dataset.tab) });
  }
  for (const p of state.overview?.profiles || []) {
    if (p.name === state.profile) continue;
    items.push({ kind: "Profile", label: `Switch to ${p.name}`, hint: p.status, run: () => {
      const sel = $("#profile");
      sel.value = p.name;
      sel.dispatchEvent(new Event("change"));
    } });
  }
  const running = isRunning();
  items.push(running
    ? { kind: "Action", label: `Stop profile ${state.profile}`, run: async () => {
      if (await confirmBox(`Stop ${state.profile}?`, "Every container in it stops with the VM.", "Stop", true)) runJob("POST", `${P()}/actions/stop`, undefined, render);
    } }
    : { kind: "Action", label: `Start profile ${state.profile}`, run: () => startProfile() });
  if (running) {
    items.push({ kind: "Action", label: `Restart profile ${state.profile}`, run: async () => {
      if (await confirmBox(`Restart ${state.profile}?`, "Every container in it stops while the VM restarts.", "Restart", true)) runJob("POST", `${P()}/actions/restart`, undefined, render);
    } });
    items.push({ kind: "Action", label: "Prune unused images", run: () => pruneImages() });
  }
  items.push({ kind: "Action", label: "Run diagnostics", run: () => palGo("diagnostics") });
  items.push({ kind: "Action", label: "Toggle theme (light, dark, system)", run: () => {
    const order = ["system", "light", "dark"];
    const next = order[(order.indexOf(safeGet("theme") || "system") + 1) % 3];
    safeSet("theme", next);
    applyTheme();
    toast(`Theme: ${next}`);
  } });
  items.push({ kind: "Help", label: "Keyboard shortcuts", run: () => palHelp() });
  return items;
}

// Containers, images and pods of the current profile, fetched once per opening.
async function palLoadLists() {
  if (!isRunning()) return [];
  const out = [];
  const [cs, imgs, pods] = await Promise.all([
    api("GET", `${P()}/containers`).catch(() => []),
    api("GET", `${P()}/images`).catch(() => []),
    state.status?.status?.kubernetes || state.overview?.kubernetes
      ? api("GET", `${P()}/kubernetes/pods`).catch(() => [])
      : api("GET", `${P()}/status`).then((s) => s?.status?.kubernetes ? api("GET", `${P()}/kubernetes/pods`) : []).catch(() => []),
  ]);
  for (const c of cs || []) {
    const name = (c.Names[0] || "").replace(/^\//, "") || short(c.Id);
    const on = c.State === "running";
    const act = (a) => async () => {
      try { await api("POST", `${P()}/containers/${encodeURIComponent(c.Id)}/${a}`); toast(`${name}: ${a}`); if (state.tab === "containers") render(); } catch (e) { toast(e.message, true); }
    };
    out.push({ kind: "Container", label: `${name} · logs`, hint: c.State, run: () => palGo("containers", { name, open: "logs" }) });
    out.push({ kind: "Container", label: `${name} · inspect`, hint: c.State, run: () => palGo("containers", { name, open: "inspect" }) });
    if (on) out.push({ kind: "Container", label: `${name} · shell`, hint: c.State, run: () => palGo("containers", { name, open: "shell" }) });
    out.push(on
      ? { kind: "Container", label: `${name} · stop`, hint: c.State, run: async () => { if (await confirmBox(`Stop ${name}?`, "The container stops; start it again from Containers.", "Stop", true)) act("stop")(); } }
      : { kind: "Container", label: `${name} · start`, hint: c.State, run: act("start") });
    out.push({ kind: "Container", label: `${name} · restart`, hint: c.State, run: async () => { if (await confirmBox(`Restart ${name}?`, "The container stops and starts again.", "Restart", false)) act("restart")(); } });
  }
  for (const i of imgs || []) {
    for (const t of (i.RepoTags || []).filter((x) => x !== "<none>:<none>")) {
      out.push({ kind: "Image", label: t, run: () => palGo("images", { q: t }) });
    }
  }
  for (const p of pods || []) {
    out.push({ kind: "Pod", label: `${p.namespace}/${p.name}`, hint: p.phase || p.status || "", run: () => palGo("kubernetes", { ns: p.namespace, pod: p.name }) });
  }
  return out;
}

function palBuild() {
  const input = el("input", { type: "search", placeholder: "Pages, containers, images, actions…", "aria-label": "Search commands", autocomplete: "off", spellcheck: "false", role: "combobox", "aria-expanded": "true", "aria-controls": "pal-list" });
  const list = el("ul.pal-list", { id: "pal-list", role: "listbox" });
  const foot = el("div.pal-foot", el("span", el("kbd", "↑"), el("kbd", "↓"), " move"), el("span", el("kbd", "Enter"), " run"), el("span", el("kbd", "Esc"), " close"), el("span.pal-more", "?  shortcuts"));
  const root = el("div.pal", { hidden: true },
    el("div.pal-card", { role: "dialog", "aria-modal": "true", "aria-label": "Command palette" }, input, list, foot));
  root.addEventListener("mousedown", (e) => { if (e.target === root) palClose(); });
  input.addEventListener("input", () => { pal.sel = 0; palPaint(); });
  input.addEventListener("keydown", (e) => {
    if (e.key === "ArrowDown") { e.preventDefault(); pal.sel = Math.min(pal.shown.length - 1, pal.sel + 1); palPaint(true); }
    else if (e.key === "ArrowUp") { e.preventDefault(); pal.sel = Math.max(0, pal.sel - 1); palPaint(true); }
    else if (e.key === "Enter") { e.preventDefault(); palRun(pal.shown[pal.sel]); }
    else if (e.key === "Escape") { e.preventDefault(); e.stopPropagation(); palClose(); }
  });
  document.body.append(root);
  Object.assign(pal, { root, input, list });
}

function palPaint(keepScroll) {
  const q = pal.input.value.trim().toLowerCase().replace(/\s+/g, " ");
  const scored = pal.items.map((it, n) => ({ it, s: palScore(q, `${it.label} ${it.kind}`), n })).filter((x) => x.s > 0);
  if (q) scored.sort((a, b) => b.s - a.s || a.n - b.n);
  pal.shown = scored.slice(0, 60).map((x) => x.it);
  pal.sel = Math.max(0, Math.min(pal.sel, pal.shown.length - 1));
  pal.list.replaceChildren(...pal.shown.map((it, n) => {
    const li = el("li.pal-item", { role: "option", id: `pal-${n}`, "aria-selected": String(n === pal.sel) },
      el("span.pal-label", it.label), it.hint ? el("span.pal-hint", it.hint) : null, el(`span.pal-kind.k-${it.kind.toLowerCase()}`, it.kind));
    li.addEventListener("mousemove", () => { if (pal.sel !== n) { pal.sel = n; palPaint(true); } });
    li.addEventListener("click", () => palRun(it));
    return li;
  }));
  if (!pal.shown.length) pal.list.append(el("li.pal-empty", pal.loading ? "Loading…" : "Nothing matches."));
  pal.input.setAttribute("aria-activedescendant", pal.shown.length ? `pal-${pal.sel}` : "");
  if (keepScroll) pal.list.children[pal.sel]?.scrollIntoView({ block: "nearest" });
}

async function palOpen() {
  if (!pal.root) palBuild();
  if (pal.open) { pal.input.select(); return; }
  pal.open = true;
  pal.root.hidden = false;
  pal.input.value = "";
  pal.sel = 0;
  pal.items = palBaseItems();
  pal.loading = true;
  palPaint();
  pal.input.focus();
  const token = (pal.token = (pal.token || 0) + 1);
  const more = await palLoadLists().catch(() => []);
  pal.loading = false;
  if (!pal.open || token !== pal.token) return;
  pal.items = pal.items.concat(more);
  palPaint();
}

function palClose() {
  if (!pal.root) return;
  pal.open = false;
  pal.root.hidden = true;
}

function palRun(it) {
  if (!it) return;
  palClose();
  it.run();
}

function palHelp() {
  let box = $("#pal-help");
  if (!box) {
    box = el("div.pal", { id: "pal-help" },
      el("div.pal-card.pal-help", { role: "dialog", "aria-modal": "true", "aria-labelledby": "pal-help-title" },
        el("div.pal-help-head", el("h2", { id: "pal-help-title" }, "Keyboard shortcuts"),
          el("button.ghost.mini", { type: "button", onclick: () => { box.hidden = true; } }, "Close")),
        el("dl.pal-keys", PAL_SHORTCUTS.flatMap(([k, d]) => [el("dt", k.split(" ").map((x) => el("kbd", x))), el("dd", d)]))));
    box.addEventListener("mousedown", (e) => { if (e.target === box) box.hidden = true; });
    document.body.append(box);
  }
  box.hidden = false;
  box.querySelector("button").focus();
}

let palPrefix = 0; // time "g" was pressed
document.addEventListener("keydown", (e) => {
  if ((e.metaKey || e.ctrlKey) && !e.altKey && e.key.toLowerCase() === "k" && !e.target.closest?.(".xterm")) {
    e.preventDefault();
    if (pal.open) palClose(); else palOpen();
    return;
  }
  const help = $("#pal-help");
  if (e.key === "Escape" && help && !help.hidden) { help.hidden = true; e.stopPropagation(); return; }
  if (pal.open || e.metaKey || e.ctrlKey || e.altKey || palTyping(e)) return;
  if (!$("#modal").hidden) return;
  if (palPrefix && Date.now() - palPrefix < 1200) {
    palPrefix = 0;
    const page = PAL_JUMPS[e.key];
    if (page) { e.preventDefault(); palGo(page); }
    return;
  }
  if (e.key === "/") { e.preventDefault(); palOpen(); }
  else if (e.key === "?") { e.preventDefault(); palHelp(); }
  else if (e.key === "g") { palPrefix = Date.now(); }
}, true);

// The hint in the sidebar header.
(() => {
  const mac = /Mac|iPhone|iPad/.test(navigator.platform || navigator.userAgent);
  const btn = el("button.pal-hint-btn", { type: "button", title: "Command palette (press ? for all shortcuts)", "aria-label": "Open the command palette" }, mac ? "⌘K" : "Ctrl K");
  btn.addEventListener("click", () => palOpen());
  document.querySelector(".brand")?.append(btn);
})();
