// Colima Dashboard page. Plain JavaScript, no build step. Everything from the API is put on the page
// with textContent (never innerHTML), so container names, logs and labels cannot inject markup.
"use strict";

const $ = (sel) => document.querySelector(sel);

const state = {
  profile: null,
  overview: null,
  status: null,
  tab: "general",
  timer: null,
  logSource: null,
};

// el("div.card", {onclick}, child, "text", ...) builds DOM safely.
function el(spec, attrs, ...children) {
  const [tag, ...classes] = spec.split(".");
  const node = document.createElement(tag || "div");
  if (classes.length) node.className = classes.join(" ");
  if (attrs && (typeof attrs !== "object" || attrs instanceof Node || Array.isArray(attrs))) {
    children.unshift(attrs);
    attrs = null;
  }
  for (const [k, v] of Object.entries(attrs || {})) {
    if (v === undefined || v === null || v === false) continue;
    if (k.startsWith("on")) node.addEventListener(k.slice(2), v);
    else if (k === "text") node.textContent = v;
    else if (k === "dataset") Object.assign(node.dataset, v);
    else node.setAttribute(k, v === true ? "" : String(v));
  }
  for (const c of children.flat()) {
    if (c === null || c === undefined || c === false) continue;
    node.append(c instanceof Node ? c : document.createTextNode(String(c)));
  }
  return node;
}

async function api(method, path, body) {
  const opts = { method, headers: {}, credentials: "same-origin" };
  if (body !== undefined) {
    opts.headers["Content-Type"] = "application/json";
    opts.body = JSON.stringify(body);
  }
  const res = await fetch(path, opts);
  let data = null;
  const text = await res.text();
  try { data = text ? JSON.parse(text) : null; } catch { data = { error: text.trim() }; }
  if (res.status === 401) {
    location.reload();
    throw new Error("signed out");
  }
  if (!res.ok) throw new Error((data && data.error) || `HTTP ${res.status}`);
  return data;
}

const P = () => `/api/profiles/${encodeURIComponent(state.profile)}`;

function bytes(n) {
  if (n === undefined || n === null || n < 0) return "–";
  const u = ["B", "KB", "MB", "GB", "TB"];
  let i = 0;
  while (n >= 1000 && i < u.length - 1) { n /= 1000; i++; }
  return `${n.toFixed(n >= 100 || i === 0 ? 0 : 1)} ${u[i]}`;
}
function gib(n) { return n ? `${Math.round(n / 1073741824)} GiB` : "–"; }
function ago(unix) {
  const s = Math.max(0, Date.now() / 1000 - unix);
  if (s < 90) return "just now";
  if (s < 5400) return `${Math.round(s / 60)} min ago`;
  if (s < 129600) return `${Math.round(s / 3600)} h ago`;
  return `${Math.round(s / 86400)} days ago`;
}
function short(id) { return (id || "").replace(/^sha256:/, "").slice(0, 12); }

function toast(msg, bad) {
  const t = $("#toast");
  t.textContent = msg;
  t.className = bad ? "toast bad" : "toast";
  t.hidden = false;
  clearTimeout(toast.t);
  toast.t = setTimeout(() => { t.hidden = true; }, bad ? 7000 : 3000);
}

// In-page confirmation; never window.confirm.
function confirmBox(title, body, okText, danger, typed) {
  return new Promise((resolve) => {
    const m = $("#modal");
    $("#modaltitle").textContent = title;
    const bodyEl = $("#modalbody");
    bodyEl.replaceChildren(typeof body === "string" ? el("p", body) : body);
    const ok = $("#modalok");
    ok.textContent = okText;
    ok.className = danger ? "danger" : "";
    let input = null;
    if (typed) {
      input = el("input", { "aria-label": `Type ${typed} to confirm`, autocomplete: "off" });
      bodyEl.append(el("label.field", el("span", `Type ${typed} to confirm`), input));
      ok.disabled = true;
      input.addEventListener("input", () => { ok.disabled = input.value !== typed; });
    } else {
      ok.disabled = false;
    }
    const done = (v) => {
      m.hidden = true;
      ok.onclick = null;
      $("#modalcancel").onclick = null;
      document.removeEventListener("keydown", onKey);
      resolve(v);
    };
    const onKey = (e) => { if (e.key === "Escape") done(false); };
    ok.onclick = () => done(true);
    $("#modalcancel").onclick = () => done(false);
    document.addEventListener("keydown", onKey);
    m.hidden = false;
    (input || ok).focus();
  });
}

function copyBtn(cmd) {
  const btn = el("button.ghost.mini", { type: "button" }, "Copy");
  btn.addEventListener("click", async () => {
    try { await navigator.clipboard.writeText(cmd); toast("Copied"); } catch { toast("Copy failed: select the text instead", true); }
  });
  return btn;
}

// A fix with its button. Commands open in Terminal (the server works the command out again
// from the check id); start and restart use the page's own progress view.
function fixRow(c) {
  if (!c.fix) return null;
  const label = c.action === "start" ? `Start ${state.profile}` : c.action === "restart" ? `Restart ${state.profile}` : c.action === "certs" ? "Copy certificates" : "Run in Terminal";
  const run = el(c.confirm ? "button.danger.mini" : "button.mini", { type: "button" }, label);
  run.addEventListener("click", async () => {
    if (c.confirm && !await confirmBox(`${c.title}: fix it?`,
      el("div", el("p", c.confirm), c.action === "certs" ? null : el("p.muted.small", "Runs in Terminal: "), c.action === "certs" ? null : el("pre.log", c.fix)), "Run it", true)) return;
    if (c.action === "start") return startProfile();
    if (c.action === "restart") return runJob("POST", `${P()}/actions/restart`, undefined, () => render());
    if (c.action === "certs") return runJob("POST", `${P()}/certs`, undefined, () => render());
    await openFix(run, c.id);
  });
  if (c.action === "certs") return el("div.copy", el("span.small", c.fix), run);
  return el("div.copy", el("code", c.fix), run, copyBtn(c.fix));
}

async function openFix(btn, id) {
  btn.disabled = true;
  try {
    await api("POST", "/api/fix", { profile: state.profile, id });
    state.fixPending = true;
    toast("Opened in Terminal. The checks run again when you come back here.");
  } catch (e) {
    toast(e.message, true);
  } finally {
    btn.disabled = false;
  }
}

// Back from Terminal after a fix: check again.
window.addEventListener("focus", () => {
  if (!state.fixPending) return;
  state.fixPending = false;
  if (["general", "diagnostics", "kubernetes"].includes(state.tab)) render(); else refreshDiagBadge();
});

function needsFix(c) { return (c.state === "fail" || c.state === "warn") && c.id !== "vm" && !isMuted(c); }

// Muted checks (this browser only) still run and show on Diagnostics, but leave the badge and
// General's "things to fix" alone. Keyed by check id and title, so a muted check about one
// thing does not hide a different problem under the same id.
function mutedSet() { try { return new Set(JSON.parse(safeGet("muted") || "[]")); } catch { return new Set(); } }
function muteKey(c) { return `${c.id}|${c.title}`; }
function isMuted(c) { return mutedSet().has(muteKey(c)); }
function setMuted(c, on) {
  const m = mutedSet();
  if (on) m.add(muteKey(c)); else m.delete(muteKey(c));
  safeSet("muted", JSON.stringify([...m]));
}

// ---------- jobs (long-running operations with streamed output) ----------

function followJob(job, onDone) {
  const panel = $("#jobpanel");
  const log = $("#joblog");
  const st = $("#jobstate");
  $("#jobtitle").textContent = job.title;
  st.textContent = "running";
  st.className = "pill busy";
  log.replaceChildren();
  panel.hidden = false;
  trackBegin(job);
  const src = new EventSource(`/api/jobs/${encodeURIComponent(job.id)}/events`);
  src.addEventListener("line", (e) => {
    const atBottom = log.scrollTop + log.clientHeight >= log.scrollHeight - 20;
    const line = JSON.parse(e.data);
    log.append(logfmt.line(line));
    if (atBottom) log.scrollTop = log.scrollHeight;
    trackLine(job, line);
  });
  src.addEventListener("done", (e) => {
    src.close();
    const d = JSON.parse(e.data);
    trackEnd(job, d.ok);
    st.textContent = d.ok ? "done" : "failed";
    st.className = d.ok ? "pill ok" : "pill fail";
    if (!d.ok) log.append(logfmt.line(`ERROR ${d.error}`, "", true));
    log.scrollTop = log.scrollHeight;
    if (onDone) onDone(d.ok);
    refreshOverview();
  });
  src.onerror = () => {
    // EventSource reconnects by itself; the server replays from the start, so clear first.
    log.replaceChildren();
    trackBegin(job);
  };
}
$("#jobclose").addEventListener("click", () => { $("#jobpanel").hidden = true; });

// The output panel: drag its top edge (or focus the grip and use the arrow keys) to resize;
// Minimise folds it to its title bar. Both are remembered in this browser.
(function jobPanelSizing() {
  const panel = $("#jobpanel"), grip = $("#jobgrip"), minBtn = $("#jobmin");
  const MIN = 120;
  const clamp = (h) => Math.round(Math.max(MIN, Math.min(window.innerHeight - 24, h)));
  const setH = (h, save) => {
    const v = clamp(h);
    panel.style.setProperty("--job-h", v + "px");
    if (save) safeSet("jobH", String(v));
  };
  const setMin = (on) => {
    panel.classList.toggle("min", on);
    minBtn.textContent = on ? "Expand" : "Minimise";
    minBtn.setAttribute("aria-expanded", String(!on));
    safeSet("jobMin", on ? "1" : "0");
  };
  const saved = Number(safeGet("jobH"));
  if (saved > 0) setH(saved, false);
  if (safeGet("jobMin") === "1") setMin(true);
  minBtn.addEventListener("click", () => setMin(!panel.classList.contains("min")));
  grip.addEventListener("pointerdown", (e) => {
    e.preventDefault();
    grip.setPointerCapture(e.pointerId);
    panel.classList.add("resizing");
    const startY = e.clientY, startH = panel.getBoundingClientRect().height;
    const move = (ev) => setH(startH + (startY - ev.clientY), false);
    const up = () => {
      grip.removeEventListener("pointermove", move);
      grip.removeEventListener("pointerup", up);
      grip.removeEventListener("pointercancel", up);
      panel.classList.remove("resizing");
      setH(panel.getBoundingClientRect().height, true);
    };
    grip.addEventListener("pointermove", move);
    grip.addEventListener("pointerup", up);
    grip.addEventListener("pointercancel", up);
  });
  grip.addEventListener("dblclick", () => setH(window.innerHeight * 0.45, true));
  grip.addEventListener("keydown", (e) => {
    const step = e.shiftKey ? 120 : 40;
    const h = panel.getBoundingClientRect().height;
    if (e.key === "ArrowUp") { setH(h + step, true); e.preventDefault(); }
    else if (e.key === "ArrowDown") { setH(h - step, true); e.preventDefault(); }
  });
  window.addEventListener("resize", () => { const h = Number(safeGet("jobH")); if (h > 0) setH(h, false); });
})();

async function runJob(method, path, body, onDone) {
  try {
    const job = await api(method, path, body);
    followJob(job, onDone);
  } catch (e) {
    toast(e.message, true);
  }
}

// ---------- drawer (logs, inspect) ----------

function openDrawer(title, ...content) {
  if (state.drawerCleanup) { state.drawerCleanup(); state.drawerCleanup = null; }
  $("#drawertitle").textContent = title;
  $("#drawerbody").replaceChildren(...content);
  $("#drawer").hidden = false;
}
function closeDrawer() {
  $("#drawer").hidden = true;
  if (state.logSource) { state.logSource.close(); state.logSource = null; }
  if (state.drawerCleanup) { state.drawerCleanup(); state.drawerCleanup = null; }
}
$("#drawerclose").addEventListener("click", closeDrawer);
$("#drawer").addEventListener("click", (e) => { if (e.target.id === "drawer") closeDrawer(); });
// Escape inside a terminal belongs to the program running there (vim, less), not the drawer.
document.addEventListener("keydown", (e) => { if (e.key === "Escape" && !$("#drawer").hidden && $("#modal").hidden && !e.target.closest?.(".xterm")) closeDrawer(); });

// ---------- shell: profile picker, nav, status ----------

async function refreshOverview() {
  try {
    const ov = await api("GET", "/api/overview");
    state.overview = ov;
    const sel = $("#profile");
    const names = ov.profiles.map((p) => p.name);
    if (!state.profile || !names.includes(state.profile)) {
      const saved = safeGet("profile");
      state.profile = names.includes(saved) ? saved : (names.includes("default") ? "default" : names[0] || "default");
    }
    sel.replaceChildren(...(names.length ? names : ["default"]).map((n) => el("option", { value: n, selected: n === state.profile }, n)));
    renderVMState();
    const key = `${state.profile}:${currentProfile()?.status}`;
    if (state.lastKey && state.lastKey !== key && $("#modal").hidden) render();
    state.lastKey = key;
    $("#versions").textContent = `Colima Dashboard ${ov.version}` + (ov.colimaVersion ? ` · colima ${ov.colimaVersion}` : "");
    if (state.bootVersion && ov.version !== state.bootVersion) { location.reload(); return; }
    state.bootVersion = state.bootVersion || ov.version;
    paintStatusBar();
  } catch (e) {
    $("#vmstate").replaceChildren(el("span.pill.fail", "error"));
  }
}

function currentProfile() {
  return (state.overview?.profiles || []).find((p) => p.name === state.profile);
}
function isRunning() { return currentProfile()?.status === "Running"; }

function renderVMState() {
  const p = currentProfile();
  const box = $("#vmstate");
  if (!p) { box.replaceChildren(el("span.pill", "no profile")); return; }
  const cls = p.busy ? "busy" : p.status === "Running" ? "running" : "";
  box.replaceChildren(el(`span.pill.${cls || "x"}`, p.busy ? `${p.busy}…` : p.status));
}

function safeGet(k) { try { return localStorage.getItem("colima-dashboard:" + k); } catch { return null; } }
function safeSet(k, v) { try { localStorage.setItem("colima-dashboard:" + k, v); } catch { /* private mode */ } }

$("#profile").addEventListener("change", (e) => {
  state.profile = e.target.value;
  safeSet("profile", state.profile);
  renderVMState();
  render();
  refreshDiagBadge();
  refreshStatusInfo();
});

window.addEventListener("hashchange", () => { if (!$("#drawer").hidden) closeDrawer(); render(); });

function setTimer(fn, ms) {
  clearInterval(state.timer);
  state.timer = setInterval(() => { if (!document.hidden && $("#modal").hidden) fn(); }, ms);
}

function notRunningCard() {
  const p = currentProfile();
  return el("div.card.empty",
    el("p", `Profile ${state.profile} is ${p ? p.status.toLowerCase() : "not created yet"}.`),
    el("button", { type: "button", onclick: () => startProfile() }, `Start ${state.profile}`));
}

function startProfile(opts) {
  return runJob("POST", `${P()}/actions/start`, opts || {}, () => render());
}

async function render() {
  // Hash routes take parameters: #containers?name=web-1&open=logs, #images?q=shop*.
  const [tab, qs] = (location.hash || "#general").slice(1).split("?");
  state.tab = tabs[tab] ? tab : "general";
  state.route = { page: state.tab, params: Object.fromEntries(new URLSearchParams(qs || "")) };
  for (const a of document.querySelectorAll("#nav a")) a.classList.toggle("active", a.dataset.tab === state.tab);
  clearInterval(state.timer);
  const main = $("#main");
  try {
    await tabs[state.tab](main);
  } catch (e) {
    main.replaceChildren(el("div.notice.bad", e.message));
  }
}

// ---------- General ----------

async function renderGeneral(main) {
  const p = currentProfile();
  const running = isRunning();
  const head = el("div.head",
    el("div", el("h1", "General"), el("p.muted", "Your Colima virtual machine and what is using its disk.")),
    el("div.toolbar",
      running
        ? [el("button.ghost", { type: "button", onclick: () => runJob("POST", `${P()}/actions/restart`, undefined, render) }, "Restart"),
           el("button", { type: "button", onclick: () => runJob("POST", `${P()}/actions/stop`, undefined, render) }, "Stop")]
        : el("button", { type: "button", onclick: () => startProfile() }, "Start")));
  const cards = el("div.grid");
  const boot = bootVisible() ? bootCard() : null;
  if (boot && !tracker.done) {
    // While it starts, the engine is not there yet: show the progress, not errors.
    head.lastChild.replaceChildren(el("button", { type: "button", disabled: true }, "Starting…"));
  }
  // The VM at a glance: name and state on top, then one tile per setting.
  const tone = !p ? "none" : running ? "on" : /ing$/i.test(p.status || "") ? "busy" : "off";
  const vm = el("section.card.vm-card",
    el("div.vm-head",
      el("div.vm-title", el("span.vm-label", "Profile"), el("h2", state.profile)),
      el(`span.status.${tone}`, el("i"), p ? p.status : "Not created")));
  if (p) {
    const tile = (label, value) => el("div.tile", el("span.tile-label", label), el("span.tile-value", value));
    vm.append(el("div.tiles",
      tile("Runtime", p.runtime || "–"),
      tile("Architecture", p.arch || "–"),
      tile("CPUs", String(p.cpus || "–")),
      tile("Memory", gib(p.memory)),
      tile("Disk", gib(p.disk)),
      p.address ? tile("Address", p.address) : null));
  } else {
    vm.append(el("p.muted.vm-empty", "This profile does not exist yet. Start creates it with Colima's defaults."));
  }
  cards.append(vm);
  const attention = el("div.attention");
  main.replaceChildren(head, attention, cards, ...(boot ? [boot] : [welcomeCard()]));
  api("GET", `/api/diagnostics?profile=${encodeURIComponent(state.profile)}`).then((list) => {
    setDiagBadge(list);
    const todo = list.filter(needsFix);
    if (!todo.length) return;
    attention.replaceChildren(el("section.card.fix-card",
      el("div.fix-head", el("h2", todo.length === 1 ? "1 thing to fix" : `${todo.length} things to fix`),
        el("a.small", { href: "#diagnostics" }, "All checks")),
      el("ul.fix-list", todo.map((c) => el(`li.fix-item.${c.state}`,
        el("div.fix-title", el("strong", c.title), el(`span.sev.${c.state}`, c.state === "fail" ? "Problem" : "Warning")),
        el("p.fix-detail", c.detail), fixRow(c))))));
  }).catch(() => { /* Diagnostics shows the error */ });
  if (state.overview?.colimaError) {
    main.prepend(el("div.notice.bad", el("p", "colima is not available: ", state.overview.colimaError, "."),
      fixRow({ id: "colima", title: "Install colima", fix: "brew install colima docker", action: "terminal" })));
  }
  if (!running || (boot && !tracker.done)) return;

  const st = await api("GET", `${P()}/status`);
  if (st.running) {
    const s = st.status;
    const eng = el("div.card", el("h2", "Engine"), el("dl.kv",
      el("dt", "VM"), el("dd", s.driver),
      el("dt", "Mounts"), el("dd", s.mount_type),
      el("dt", "Kubernetes"), el("dd", s.kubernetes ? "on" : "off"),
      el("dt", "Docker"), el("dd", st.engine ? `${st.engine.Version} (API ${st.engine.ApiVersion})` : (st.engineError || "–")),
      el("dt", "Socket"), el("dd.mono.small", (s.docker_socket || "").replace("unix://", ""))));
    cards.append(eng);
  }
  const disk = el("div.card", el("h2", "Disk use"), el("p.muted.small", "Loading…"));
  cards.append(disk);
  api("GET", `${P()}/df`).then((rows) => {
    const max = Math.max(1, ...rows.map((r) => r.size));
    const total = rows.reduce((a, r) => a + r.size, 0);
    const reclaim = rows.reduce((a, r) => a + r.reclaimable, 0);
    disk.replaceChildren(el("h2", "Disk use"),
      el("div.bars", rows.map((r) => {
        const fill = el("div.fill");
        fill.style.width = `${(r.size / max) * 100}%`;
        return el("div.bar", el("span", `${r.type} (${r.count})`), el("div.track", fill), el("span.mono", bytes(r.size)));
      })),
      el("p.small.muted", `${bytes(total)} in use, ${bytes(reclaim)} reclaimable by pruning.`));
  }).catch((e) => disk.replaceChildren(el("h2", "Disk use"), el("p.muted", e.message)));
}

// ---------- Containers ----------

async function renderContainers(main) {
  const head = el("div.head",
    el("div", el("h1", "Containers"), el("p.muted", "Running and stopped containers, grouped by Compose project.")),
    el("div.toolbar", el("input", { type: "search", id: "cfilter", placeholder: "Filter", "aria-label": "Filter containers", value: state.route?.params.q ?? safeGet("cfilter") ?? "" })));
  if (!isRunning()) { main.replaceChildren(head, notRunningCard()); return; }
  const body = el("div.card.table-wrap", el("p.muted", "Loading…"));
  main.replaceChildren(head, body);
  const filter = $("#cfilter");
  let list = [];
  const draw = () => {
    const q = filter.value.trim().toLowerCase();
    safeSet("cfilter", filter.value);
    const rows = list.filter((c) => !q || [c.Names.join(" "), c.Image, c.State, c.Labels?.["com.docker.compose.project"] || ""].join(" ").toLowerCase().includes(q));
    if (!rows.length) { body.replaceChildren(el("p.empty", list.length ? "Nothing matches the filter." : "No containers yet. Try: docker run -d -p 8080:80 nginx")); return; }
    const groups = new Map();
    for (const c of rows) {
      const g = c.Labels?.["com.docker.compose.project"] || "";
      if (!groups.has(g)) groups.set(g, []);
      groups.get(g).push(c);
    }
    const tbody = el("tbody");
    const keys = [...groups.keys()].sort((a, b) => (a === "") - (b === "") || a.localeCompare(b));
    for (const g of keys) {
      const cs = groups.get(g);
      if (g) {
        const anyRunning = cs.some((c) => c.State === "running");
        tbody.append(el("tr.group", el("td", { colspan: 6 }, `Compose: ${g}`, el("span.muted.small", ` · ${cs.length} ${cs.length === 1 ? "container" : "containers"}`)),
          el("td.actions",
            el("button.ghost.mini", { type: "button", onclick: () => showProjectLogs(g), title: "All of this project's logs, interleaved by time" }, "Logs"),
            el("button.ghost.mini", { type: "button", onclick: () => groupAction(cs, anyRunning ? "stop" : "start") }, anyRunning ? "Stop all" : "Start all"))));
      }
      for (const c of cs) tbody.append(containerRow(c));
    }
    body.replaceChildren(el("table.ctable",
      el("thead", el("tr", ["Name", "Image", "State", "Limits", "Ports", "Created", ""].map((h) => el("th", h)))), tbody));
  };
  const load = async () => {
    try { list = await api("GET", `${P()}/containers`); draw(); } catch (e) { body.replaceChildren(el("div.notice.bad", e.message)); }
  };
  filter.addEventListener("input", draw);
  await load();
  openRouted(list);
  setTimer(load, 4000);
}

// #containers?name=x&open=logs|inspect|shell opens that container's drawer once per navigation.
function openRouted(list) {
  const { name, open } = state.route?.params || {};
  if (!name || state.route.done) return;
  state.route.done = true;
  const c = list.find((x) => x.Names.some((n) => n.replace(/^\//, "") === name) || x.Id.startsWith(name));
  if (!c) { toast(`No container called ${name}`, true); return; }
  if (open === "shell" && c.State === "running") showShell(c.Id, name);
  else if (open === "inspect") showInspect(c.Id, name);
  else showLogs(c.Id, name);
}

function linkBtn(page, params) {
  const btn = el("button.ghost.mini", { type: "button", title: "Copy a link that opens this again" }, "Copy link");
  btn.addEventListener("click", async () => {
    const url = `${location.origin}${location.pathname}#${page}?${new URLSearchParams(params)}`;
    try { await navigator.clipboard.writeText(url); toast("Link copied"); } catch { toast("Copy failed", true); }
  });
  return btn;
}

function containerRow(c) {
  const name = (c.Names[0] || "").replace(/^\//, "") || short(c.Id);
  const ports = [];
  const seen = new Set();
  for (const p of c.Ports || []) {
    if (!p.PublicPort || seen.has(p.PublicPort + p.Type)) continue;
    seen.add(p.PublicPort + p.Type);
    ports.push(p.Type === "tcp"
      ? el("a", { href: `http://localhost:${p.PublicPort}`, target: "_blank", rel: "noopener noreferrer" }, `${p.PublicPort}→${p.PrivatePort}`)
      : el("span", `${p.PublicPort}→${p.PrivatePort}/${p.Type}`), " ");
  }
  const act = (a) => async () => {
    try { await api("POST", `${P()}/containers/${encodeURIComponent(c.Id)}/${a}`); toast(`${name}: ${a}`); render(); } catch (e) { toast(e.message, true); }
  };
  const running = c.State === "running";
  const paused = c.State === "paused";
  return el("tr",
    el("td", el("strong", name), el("div.muted.small.mono", short(c.Id))),
    // an image whose tag moved on shows as a digest: keep it short, full value on hover
    el("td.mono.small.trunc", { title: c.Image }, c.Image.startsWith("sha256:") ? c.Image.slice(0, 19) + "…" : c.Image),
    el("td", el(`span.pill.${c.State}`, c.State), el("div.muted.small.nowrap", c.Status), containerFlags(c)),
    limitsCell(c),
    el("td", ports.length ? ports : el("span.muted", "–")),
    el("td.small", ago(c.Created)),
    el("td.actions.wrap",
      running ? el("button.ghost.mini", { type: "button", onclick: act("stop") }, "Stop") : null,
      running ? el("button.ghost.mini", { type: "button", onclick: act("pause") }, "Pause") : null,
      paused ? el("button.ghost.mini", { type: "button", onclick: act("unpause") }, "Resume") : null,
      !running && !paused ? el("button.ghost.mini", { type: "button", onclick: act("start") }, "Start") : null,
      el("button.ghost.mini", { type: "button", onclick: act("restart") }, "Restart"),
      running ? el("button.ghost.mini", { type: "button", onclick: () => showShell(c.Id, name) }, "Shell") : null,
      running ? el("button.ghost.mini", { type: "button", onclick: () => showFiles(c.Id, name) }, "Files") : null,
      el("button.ghost.mini", { type: "button", onclick: () => showLogs(c.Id, name) }, "Logs"),
      el("button.ghost.mini", { type: "button", onclick: () => showInspect(c.Id, name) }, "Inspect"),
      el("button.ghost.mini", { type: "button", onclick: () => showLimits(c, name) }, "Limits"),
      el("button.ghost.mini.danger", { type: "button", onclick: () => removeContainer(c, name) }, "Remove")));
}

async function groupAction(cs, action) {
  for (const c of cs) {
    try { await api("POST", `${P()}/containers/${encodeURIComponent(c.Id)}/${action}`); } catch (e) { toast(e.message, true); }
  }
  render();
}

async function removeContainer(c, name) {
  const running = c.State === "running";
  if (!await confirmBox(`Remove ${name}?`, running ? "It is running; it will be stopped and removed. Its anonymous data goes with it." : "The container is deleted. Named volumes stay.", "Remove", true)) return;
  try { await api("DELETE", `${P()}/containers/${encodeURIComponent(c.Id)}?force=${running ? 1 : 0}`); toast(`Removed ${name}`); render(); } catch (e) { toast(e.message, true); }
}

// showLogs and showProjectLogs live in containers.js.

// ---------- shell and files inside a container ----------

let xtermLoad = null;
function loadXterm() {
  if (window.Terminal && window.FitAddon) return Promise.resolve();
  const add = (src) => new Promise((ok, fail) => {
    const sc = document.createElement("script");
    sc.src = src; sc.onload = ok; sc.onerror = () => fail(new Error(`could not load ${src}`));
    document.head.append(sc);
  });
  xtermLoad ??= (async () => {
    document.head.append(el("link", { rel: "stylesheet", href: "/static/vendor/xterm.css" }));
    await add("/static/vendor/xterm.js");
    await add("/static/vendor/addon-fit.js");
  })();
  return xtermLoad;
}

async function showShell(id, name) {
  const box = el("div.term");
  const status = el("span.term-status", el("i"), "Connecting…");
  const setStatus = (text, tone) => { status.className = `term-status ${tone || ""}`; status.replaceChildren(el("i"), text); };
  openDrawer(`Shell: ${name}`,
    el("div.term-bar", status, el("span.muted.small", "bash, or sh when the image has no bash. Closing this panel ends the shell.")), box);
  try { await loadXterm(); } catch (e) { setStatus(e.message, "bad"); return; }
  const term = new window.Terminal({
    cursorBlink: true, fontSize: 13, scrollback: 5000,
    fontFamily: "ui-monospace, SFMono-Regular, Menlo, Consolas, monospace",
    // a GitHub-dark style palette: the standard blue is unreadable on a near-black background
    theme: { background: "#0d1117", foreground: "#e6edf3", cursor: "#5eead4", selectionBackground: "#264f78",
      black: "#484f58", red: "#ff7b72", green: "#3fb950", yellow: "#d29922", blue: "#58a6ff", magenta: "#bc8cff", cyan: "#39c5cf", white: "#b1bac4",
      brightBlack: "#6e7681", brightRed: "#ffa198", brightGreen: "#56d364", brightYellow: "#e3b341", brightBlue: "#79c0ff", brightMagenta: "#d2a8ff", brightCyan: "#56d4dd", brightWhite: "#ffffff" },
  });
  const fit = new window.FitAddon.FitAddon();
  term.loadAddon(fit);
  // xterm.js adds <style> elements; the page's CSP admits them only with this page load's nonce.
  const nonce = document.querySelector('meta[name="style-nonce"]')?.content || "";
  const create = document.createElement;
  document.createElement = function (tag, ...rest) {
    const node = create.call(document, tag, ...rest);
    if (String(tag).toLowerCase() === "style") node.nonce = nonce;
    return node;
  };
  try { term.open(box); } finally { document.createElement = create; }
  fit.fit();
  term.focus();
  let sid, src, ro;
  let closed = false;
  state.drawerCleanup = () => {
    closed = true;
    ro?.disconnect();
    src?.close();
    if (sid) fetch(`/api/shells/${sid}`, { method: "DELETE", credentials: "same-origin" }).catch(() => {});
    term.dispose();
  };
  try {
    ({ id: sid } = await api("POST", `${P()}/containers/${encodeURIComponent(id)}/shell`, { cols: term.cols, rows: term.rows, name }));
  } catch (e) { setStatus(e.message, "bad"); return; }
  if (closed) { fetch(`/api/shells/${sid}`, { method: "DELETE", credentials: "same-origin" }).catch(() => {}); return; }
  setStatus("Connected", "on");
  src = new EventSource(`/api/shells/${sid}/stream`);
  src.addEventListener("out", (e) => term.write(Uint8Array.from(atob(JSON.parse(e.data)), (c) => c.charCodeAt(0))));
  src.addEventListener("exit", (e) => {
    src.close();
    setStatus(`Ended (exit code ${JSON.parse(e.data)})`, "");
    term.write("\r\n\x1b[2m[shell ended]\x1b[0m\r\n");
  });
  src.onerror = () => { if (!closed && src.readyState === EventSource.CLOSED) setStatus("Disconnected", "bad"); };
  // Keystrokes go in order, batched while a send is in flight.
  let pending = "", sending = false;
  const flush = async () => {
    if (sending || !pending || closed) return;
    sending = true;
    const data = pending;
    pending = "";
    try { await api("POST", `/api/shells/${sid}/input`, { data }); } catch (e) { setStatus(e.message, "bad"); }
    sending = false;
    flush();
  };
  term.onData((d) => { pending += d; flush(); });
  term.onResize(({ cols, rows }) => { api("POST", `/api/shells/${sid}/resize`, { cols, rows }).catch(() => {}); });
  ro = new ResizeObserver(() => { try { fit.fit(); } catch { /* panel closing */ } });
  ro.observe(box);
}

// showFiles browses a running container's files, or with kind "volume" a volume's (read in the VM).
function showFiles(id, name, kind = "container") {
  const base = kind === "volume" ? `${P()}/volumes/${encodeURIComponent(id)}` : `${P()}/containers/${encodeURIComponent(id)}`;
  const crumbs = el("nav.crumbs", { "aria-label": "Folder" });
  const pathBox = el("input.mono", { type: "text", "aria-label": "Go to path", spellcheck: false });
  const list = el("div.files");
  const viewer = el("div.viewer", { hidden: true });
  const dl = (p, kind) => el("a.btn.ghost.mini", { href: `${base}/download?path=${encodeURIComponent(p)}&kind=${kind}`, download: "" },
    kind === "file" ? "Download" : "Download .tar");
  const join = (dir, n) => (dir === "/" ? "" : dir) + "/" + n;
  const go = async (dir) => {
    pathBox.value = dir;
    viewer.hidden = true;
    list.replaceChildren(el("p.muted.small", "Loading…"));
    const parts = dir.split("/").filter(Boolean);
    crumbs.replaceChildren(el("button.link", { type: "button", onclick: () => go("/") }, "/"),
      // the root button is already "/", so the first folder needs no separator of its own
      ...parts.flatMap((p, i) => [...(i ? [el("span.muted", "/")] : []),
        el("button.link", { type: "button", onclick: () => go("/" + parts.slice(0, i + 1).join("/")) }, p)]));
    let d;
    try { d = await api("GET", `${base}/files?path=${encodeURIComponent(dir)}`); }
    catch (e) {
      list.replaceChildren(el("div.notice", el("p", e.message), el("p.small", "You can still download this folder as a .tar: "), dl(dir, "dir")));
      return;
    }
    const kindOrder = { dir: 0, dirlink: 0, link: 1, file: 2 };
    d.entries.sort((a, b) => kindOrder[a.kind] - kindOrder[b.kind] || a.name.localeCompare(b.name));
    const icon = (k) => el(`span.ficon.${k}`, k === "dir" ? "▸" : k === "dirlink" ? "↪▸" : k === "link" ? "↪" : "•");
    const rows = d.entries.map((f) => {
      const p = join(d.path, f.name);
      const isDir = f.kind === "dir" || f.kind === "dirlink";
      const open = isDir
        ? () => go(p)
        : () => view(p, f);
      return el("tr",
        el("td", icon(f.kind), " ", el("button.link.mono", { type: "button", onclick: open }, f.name)),
        el("td.small.num", isDir ? "–" : bytes(f.size)),
        el("td.small.muted", f.modified ? new Date(f.modified * 1000).toLocaleString(undefined, { dateStyle: "medium", timeStyle: "short" }) : "–"),
        el("td.actions", isDir ? dl(p, "dir") : dl(p, "file")));
    });
    list.replaceChildren(
      el("div.files-head", el("span.muted.small", `${d.entries.length} item${d.entries.length === 1 ? "" : "s"}${d.truncated ? " (first 2000 shown)" : ""}`), dl(d.path, "dir")),
      d.entries.length
        ? el("div.table-wrap", el("table.files-table",
          el("thead", el("tr", el("th", "Name"), el("th", "Size"), el("th", "Modified"), el("th"))),
          shortBody(rows, 200)))
        : el("p.muted", "This folder is empty."));
  };
  const view = async (p, f) => {
    viewer.hidden = false;
    viewer.replaceChildren(el("p.muted.small", "Opening…"));
    viewer.scrollIntoView({ block: "nearest" });
    try {
      const v = await api("GET", `${base}/file?path=${encodeURIComponent(p)}`);
      viewer.replaceChildren(
        el("div.viewer-head", el("strong.mono", p), el("span.muted.small", bytes(v.size)), dl(p, "file"),
          el("button.ghost.mini", { type: "button", onclick: () => { viewer.hidden = true; } }, "Close")),
        v.binary ? el("p.muted", "A binary file; download it to open it.")
          : el("pre.log.file-text", v.text, v.truncated ? el("span.muted", `\n… first 512 KB of ${bytes(v.size)} shown`) : null));
    } catch (e) { viewer.replaceChildren(el("p.bad-text", e.message)); }
  };
  pathBox.addEventListener("keydown", (e) => { if (e.key === "Enter" && pathBox.value.startsWith("/")) go(pathBox.value); });
  openDrawer(kind === "volume" ? `Volume: ${name}` : `Files: ${name}`, el("div.files-bar", crumbs, pathBox), viewer, list);
  go("/");
}

function lineNode(l, q) {
  return logfmt.line(l.t, q, l.s === "stderr");
}

function download(name, text) {
  const a = el("a", { href: URL.createObjectURL(new Blob([text], { type: "text/plain" })), download: name });
  document.body.append(a);
  a.click();
  setTimeout(() => { URL.revokeObjectURL(a.href); a.remove(); }, 1000);
}

async function showInspect(id, name) {
  openDrawer(`Inspect: ${name}`, el("p.muted", "Loading…"));
  $("#drawertitle").append(" ", linkBtn("containers", { name, open: "inspect" }));
  try {
    const [doc, stats] = await Promise.all([
      api("GET", `${P()}/containers/${encodeURIComponent(id)}`),
      api("GET", `${P()}/containers/${encodeURIComponent(id)}/stats`).catch(() => null),
    ]);
    const cfg = doc.Config || {};
    const host = doc.HostConfig || {};
    const parts = [];
    if (stats && doc.State?.Running) parts.push(liveStats(id, stats));
    parts.push(el("div.card", el("h2", "Container"), el("dl.kv",
      el("dt", "Image"), el("dd.mono", cfg.Image || ""),
      el("dt", "Command"), el("dd.mono", [...(cfg.Entrypoint || []), ...(cfg.Cmd || [])].join(" ") || "–"),
      el("dt", "Restart policy"), el("dd", host.RestartPolicy?.Name || "no"),
      el("dt", "Started"), el("dd", doc.State?.StartedAt || "–"),
      el("dt", "Networks"), el("dd", Object.keys(doc.NetworkSettings?.Networks || {}).join(", ") || "–"))));
    const env = (cfg.Env || []).map((e) => {
      const i = e.indexOf("=");
      return [e.slice(0, i), e.slice(i + 1)];
    });
    if (env.length) {
      const tbody = el("tbody", env.map(([k, v]) => {
        const val = el("span.mono", "••••••");
        const btn = el("button.ghost.mini", { type: "button" }, "Show");
        btn.addEventListener("click", () => {
          const hidden = btn.textContent === "Show";
          val.textContent = hidden ? v : "••••••";
          btn.textContent = hidden ? "Hide" : "Show";
        });
        return el("tr", el("td.mono", k), el("td", val), el("td.actions", btn));
      }));
      parts.push(el("div.card", el("h2", "Environment"), el("p.small.muted", "Values are hidden until you show them: they often hold secrets."), el("table", tbody)));
    }
    const mounts = doc.Mounts || [];
    if (mounts.length) {
      parts.push(el("div.card", el("h2", "Mounts"), el("table",
        el("thead", el("tr", ["Type", "Source", "Destination", "Mode"].map((h) => el("th", h)))),
        el("tbody", mounts.map((m) => el("tr", el("td", m.Type), el("td.mono.small", m.Name || m.Source), el("td.mono.small", m.Destination), el("td", m.RW ? "rw" : "ro")))))));
    }
    const ports = Object.entries(doc.NetworkSettings?.Ports || {});
    if (ports.length) {
      parts.push(el("div.card", el("h2", "Ports"), el("dl.kv", ports.flatMap(([k, v]) => [el("dt.mono", k), el("dd.mono", (v || []).map((b) => `${b.HostIp || "0.0.0.0"}:${b.HostPort}`).join(", ") || "not published")]))));
    }
    const labels = Object.entries(cfg.Labels || {});
    if (labels.length) {
      parts.push(el("div.card", el("h2", "Labels"), el("dl.kv", labels.flatMap(([k, v]) => [el("dt.mono.small", k), el("dd.mono.small", v)]))));
    }
    $("#drawerbody").replaceChildren(...parts);
  } catch (e) {
    $("#drawerbody").replaceChildren(el("div.notice.bad", e.message));
  }
}

// Live stats for a running container: CPU, memory, network and disk rates every 2 seconds,
// with a minute of history, and its processes. Stops when the drawer closes.
function spark(values, max) {
  const NS = "http://www.w3.org/2000/svg";
  const svg = document.createElementNS(NS, "svg");
  svg.setAttribute("viewBox", "0 0 120 28");
  svg.setAttribute("preserveAspectRatio", "none");
  svg.classList.add("spark");
  const top = Math.max(max || 0, ...values, 1e-9);
  const pts = values.map((v, i) => `${(i / Math.max(values.length - 1, 1)) * 120},${27 - (v / top) * 25}`).join(" ");
  const line = document.createElementNS(NS, "polyline");
  line.setAttribute("points", values.length > 1 ? pts : "0,27 120,27");
  svg.append(line);
  return svg;
}
function liveStats(id, first) {
  const N = 30;
  const hist = { cpu: [], mem: [], rx: [], tx: [], rd: [], wr: [] };
  const tiles = {};
  const tile = (key, label) => {
    tiles[key] = { val: el("strong"), chart: el("div.spark-box") };
    return el("div.live-tile", el("span.muted.small", label), tiles[key].val, tiles[key].chart);
  };
  const grid = el("div.live-grid", tile("cpu", "CPU"), tile("mem", "Memory"), tile("net", "Network in / out"), tile("disk", "Disk read / write"));
  const procs = el("div");
  const pidsNote = el("span.muted.small");
  const card = el("div.card", el("div.card-head", el("h2", "Live"), pidsNote), grid,
    el("details.procs", el("summary", "Processes"), procs));
  let prev = first, prevT = performance.now(), timer = null, stopped = false;
  const rate = (a, b, dt) => (b >= a ? (b - a) / dt : 0);
  const paint = (st, dt) => {
    hist.cpu.push(st.cpuPercent); hist.mem.push(st.memoryBytes);
    if (dt) {
      hist.rx.push(rate(prev.netRx, st.netRx, dt)); hist.tx.push(rate(prev.netTx, st.netTx, dt));
      hist.rd.push(rate(prev.blockRead, st.blockRead, dt)); hist.wr.push(rate(prev.blockWrite, st.blockWrite, dt));
    }
    for (const k of Object.keys(hist)) if (hist[k].length > N) hist[k].shift();
    const last = (a) => a[a.length - 1] || 0;
    tiles.cpu.val.textContent = `${st.cpuPercent.toFixed(1)} %`;
    tiles.cpu.chart.replaceChildren(spark(hist.cpu, 100));
    tiles.mem.val.textContent = `${bytes(st.memoryBytes)} of ${bytes(st.memoryLimit)}`;
    tiles.mem.chart.replaceChildren(spark(hist.mem));
    tiles.net.val.textContent = dt ? `${bytes(last(hist.rx))}/s · ${bytes(last(hist.tx))}/s` : "measuring…";
    tiles.net.chart.replaceChildren(spark(hist.rx.map((v, i) => v + (hist.tx[i] || 0))));
    tiles.disk.val.textContent = dt ? `${bytes(last(hist.rd))}/s · ${bytes(last(hist.wr))}/s` : "measuring…";
    tiles.disk.chart.replaceChildren(spark(hist.rd.map((v, i) => v + (hist.wr[i] || 0))));
    pidsNote.textContent = st.pids ? `${st.pids} process${st.pids === 1 ? "" : "es"} · every 2 s` : "every 2 s";
  };
  const loadTop = async () => {
    try {
      const t = await api("GET", `${P()}/containers/${encodeURIComponent(id)}/top`);
      procs.replaceChildren(el("div.table-wrap", el("table.small",
        el("thead", el("tr", (t.titles || []).map((h) => el("th", h)))),
        el("tbody", (t.processes || []).map((row) => el("tr", row.map((c, i) => el(i === row.length - 1 ? "td.mono" : "td", c))))))));
    } catch (e) { procs.replaceChildren(el("p.muted.small", e.message)); }
  };
  const tick = async () => {
    if (stopped) return;
    try {
      const st = await api("GET", `${P()}/containers/${encodeURIComponent(id)}/stats`);
      const now = performance.now();
      paint(st, (now - prevT) / 1000);
      prev = st; prevT = now;
      if (card.querySelector("details.procs").open) loadTop();
    } catch { /* stopped container or engine gone: keep the last figures */ }
    if (!stopped) timer = setTimeout(tick, 2000);
  };
  card.querySelector("details.procs").addEventListener("toggle", (e) => { if (e.target.open) loadTop(); });
  paint(first, 0);
  timer = setTimeout(tick, 2000);
  state.drawerCleanup = () => { stopped = true; clearTimeout(timer); };
  return card;
}

// ---------- Images ----------

// Build an image from a folder on the Mac, with the docker CLI (so BuildKit and its cache apply).
function buildCard() {
  const dir = el("input.mono", { placeholder: "~/code/my-app", "aria-label": "Folder to build", value: safeGet("buildDir") || "", spellcheck: false });
  const tag = el("input.mono", { placeholder: "my-app:dev (optional)", "aria-label": "Image name", value: safeGet("buildTag") || "", spellcheck: false });
  const file = el("input.mono", { placeholder: "Dockerfile (optional)", "aria-label": "Dockerfile", spellcheck: false });
  const go = el("button", { type: "button" }, "Build");
  const run = () => {
    if (!dir.value.trim()) { dir.focus(); return; }
    safeSet("buildDir", dir.value.trim()); safeSet("buildTag", tag.value.trim());
    runJob("POST", `${P()}/images/build`, { dir: dir.value.trim(), tag: tag.value.trim(), dockerfile: file.value.trim() }, () => render());
  };
  go.addEventListener("click", run);
  for (const i of [dir, tag, file]) i.addEventListener("keydown", (e) => { if (e.key === "Enter") run(); });
  const d = el("details.card.build-card",
    el("summary", el("strong", "Build an image"), el("span.muted.small", " from a folder on this Mac")),
    el("div.build-form", dir, tag, file, go),
    el("p.muted.small", "Runs docker build against this profile. The Builds page keeps its timing and cache history."));
  if (safeGet("buildOpen") === "1") d.open = true;
  d.addEventListener("toggle", () => safeSet("buildOpen", d.open ? "1" : "0"));
  return d;
}

async function pushImage(tags) {
  let ref = tags[0];
  const pick = tags.length > 1 ? el("select", { "aria-label": "Tag to push" }, tags.map((t) => el("option", { value: t }, t))) : null;
  if (pick) pick.addEventListener("change", () => { ref = pick.value; });
  const ok = await confirmBox("Push image?", el("div",
    pick ? el("p", "Tag: ", pick) : el("p", el("code", ref)),
    el("p.muted.small", "Uploads to the registry in the name (Docker Hub when there is none), with the credentials of docker login on this Mac.")), "Push");
  if (!ok) return;
  runJob("POST", `${P()}/images/push`, { ref }, () => {});
}

async function renderImages(main) {
  const ref = el("input", { placeholder: "nginx:latest", "aria-label": "Image to pull", size: 28 });
  const pull = el("button", { type: "button" }, "Pull");
  const doPull = () => {
    const r = ref.value.trim();
    if (!r) { ref.focus(); return; }
    runJob("POST", `${P()}/images/pull`, { ref: r }, () => render());
  };
  pull.addEventListener("click", doPull);
  ref.addEventListener("keydown", (e) => { if (e.key === "Enter") doPull(); });
  const head = el("div.head",
    el("div", el("h1", "Images"), el("p.muted", "Pull, build, push, scan, remove and prune images.")),
    el("div.toolbar", ref, pull, el("button.ghost", { type: "button", onclick: pruneImages }, "Prune unused")));
  if (!isRunning()) { main.replaceChildren(head, notRunningCard()); return; }
  const body = el("div.card.table-wrap", el("p.muted", "Loading…"));
  main.replaceChildren(head, buildCard(), body);
  const imgs = await api("GET", `${P()}/images`);
  if (!imgs.length) { body.replaceChildren(el("p.empty", "No images yet.")); return; }
  const items = imgs.map((i) => {
    const tags = (i.RepoTags || []).filter((t) => t !== "<none>:<none>");
    const label = tags[0] || (i.RepoDigests?.[0] || "").split("@")[0] + " (untagged)";
    return { i, tags, label, hay: [...tags, label, short(i.Id)].map((t) => t.toLowerCase()) };
  });
  // picked maps an image ID to the tags to remove, or null for the whole image. When the filter
  // matches only some of an image's tags (cdtest* on an image also tagged busybox), only those go.
  const picked = new Map();
  const pickTags = (x) => {
    const m = imageMatcher(filter.value);
    const hit = x.tags.filter((t) => m([t.toLowerCase()]));
    return filter.value.trim() && hit.length && hit.length < x.tags.length ? hit : null;
  };
  const filter = el("input", { type: "search", placeholder: "Filter, e.g. shop* or *:dev", "aria-label": "Filter images", value: state.route?.params.q ?? safeGet("ifilter") ?? "", size: 30 });
  const all = el("input", { type: "checkbox", "aria-label": "Select all shown images" });
  const count = el("span.small.muted");
  const bulk = el("button.ghost.mini.danger", { type: "button", disabled: true }, "Remove selected");
  const clear = el("button.ghost.mini", { type: "button", disabled: true }, "Clear");
  const tbody = el("tbody");
  let shown = [];
  const sync = () => {
    const n = picked.size;
    const size = items.filter((x) => picked.has(x.i.Id) && !picked.get(x.i.Id)).reduce((a, x) => a + x.i.Size, 0);
    count.textContent = n ? `${n} selected${size ? `, ${bytes(size)}` : ""}` : `${shown.length} of ${items.length} shown`;
    bulk.disabled = clear.disabled = !n;
    bulk.textContent = n ? `Remove ${n} selected` : "Remove selected";
    const on = shown.filter((x) => picked.has(x.i.Id)).length;
    all.checked = shown.length > 0 && on === shown.length;
    all.indeterminate = on > 0 && on < shown.length;
  };
  const draw = () => {
    safeSet("ifilter", filter.value);
    const m = imageMatcher(filter.value);
    shown = items.filter((x) => m(x.hay));
    tbody.replaceChildren(...shown.map(({ i, tags, label }) => {
      const box = el("input", { type: "checkbox", checked: picked.has(i.Id), "aria-label": `Select ${label}` });
      box.addEventListener("change", () => { box.checked ? picked.set(i.Id, pickTags(items.find((x) => x.i.Id === i.Id))) : picked.delete(i.Id); sync(); });
      return el("tr",
        el("td.pick", box),
        el("td", el("strong.mono", label), tags.length > 1 ? el("div.muted.small.mono", tags.slice(1).join(", ")) : null),
        el("td.mono.small", short(i.Id)),
        el("td", bytes(i.Size)),
        el("td.small", ago(i.Created)),
        el("td", i.Containers > 0 ? el("span.pill.running", `in use (${i.Containers})`) : el("span.pill", "unused")),
        el("td.actions",
          el("button.ghost.mini", { type: "button", onclick: () => scanImage(tags[0] || i.Id) }, "Scan"),
          tags.length ? el("button.ghost.mini", { type: "button", onclick: () => pushImage(tags) }, "Push") : null,
          el("button.ghost.mini.danger", { type: "button", onclick: () => removeImage(i, label) }, "Remove")));
    }));
    if (!shown.length) tbody.append(el("tr", el("td.muted", { colspan: 7 }, "No image matches the filter.")));
    sync();
  };
  all.addEventListener("change", () => { for (const x of shown) all.checked ? picked.set(x.i.Id, pickTags(x)) : picked.delete(x.i.Id); draw(); });
  clear.addEventListener("click", () => { picked.clear(); draw(); });
  filter.addEventListener("input", draw);
  bulk.addEventListener("click", () => removeImages(items.filter((x) => picked.has(x.i.Id)).map((x) => ({ ...x, only: picked.get(x.i.Id) }))));
  body.replaceChildren(
    el("div.toolbar.bulkbar", filter, count, el("span.grow"), clear, bulk),
    el("table", el("thead", el("tr", el("th.pick", all), ...["Image", "ID", "Size", "Created", "Used by", ""].map((h) => el("th", h)))), tbody));
  draw();
}

// imageMatcher turns the filter into a test over an image's names and ID. * and ? are wildcards
// matched against a whole name ("shop*" is names starting with shop); without them it is a
// plain "contains" match. Several words separated by spaces or commas match any of them.
function imageMatcher(q) {
  const words = q.toLowerCase().split(/[\s,]+/).filter(Boolean);
  if (!words.length) return () => true;
  const tests = words.map((w) => {
    if (!/[*?]/.test(w)) return (h) => h.includes(w);
    const re = new RegExp("^" + w.replace(/[.+^${}()|[\]\\]/g, "\\$&").replace(/\*/g, ".*").replace(/\?/g, ".") + "$");
    return (h) => re.test(h);
  });
  return (hay) => tests.some((t) => hay.some(t));
}

// Each entry goes whole (by ID; an unused image needs force when it has several tags, an image in
// use is never forced) or, when x.only lists tags, just those tags are removed and the image stays.
async function removeImages(list) {
  const whole = list.filter((x) => !x.only);
  const inUse = whole.filter((x) => x.i.Containers > 0);
  const size = whole.reduce((a, x) => a + x.i.Size, 0);
  const label = (x) => x.only ? `${x.only.join(", ")} (tag${x.only.length === 1 ? "" : "s"} only; ${x.tags.filter((t) => !x.only.includes(t)).join(", ")} stays)` : x.label;
  const names = list.slice(0, 8).map(label).join("; ") + (list.length > 8 ? `; and ${list.length - 8} more` : "");
  if (!await confirmBox(`Remove ${list.length} image${list.length === 1 ? "" : "s"}${size ? ` (${bytes(size)})` : ""}?`,
    `${names}.${inUse.length ? ` ${inUse.length} ${inUse.length === 1 ? "is" : "are"} used by containers and will be skipped unless those containers are removed first.` : ""}`,
    "Remove", true)) return;
  let ok = 0; const failed = [];
  for (const x of list) {
    try {
      if (x.only) for (const t of x.only) await api("DELETE", `${P()}/images/${encodeURIComponent(t)}`);
      else await api("DELETE", `${P()}/images/${encodeURIComponent(x.i.Id)}${x.i.Containers > 0 ? "" : "?force=1"}`);
      ok++;
    }
    catch (e) { failed.push(`${x.label}: ${e.message}`); }
  }
  toast(failed.length ? `Removed ${ok}; ${failed.length} not removed. ${failed[0]}` : `Removed ${ok} of ${list.length}`, failed.length > 0);
  render();
}

async function removeImage(i, label) {
  const inUse = i.Containers > 0;
  if (!await confirmBox(`Remove ${label}?`, inUse ? "Containers use this image; remove them first, or the engine refuses." : "The image is deleted from this profile; pull it again to get it back.", "Remove", true)) return;
  try { await api("DELETE", `${P()}/images/${encodeURIComponent(i.Id)}${inUse ? "" : "?force=1"}`); toast(`Removed ${label}`); render(); } catch (e) { toast(e.message, true); }
}

async function pruneImages() {
  if (!await confirmBox("Prune unused images?", "Every image no container uses is deleted, tagged or not.", "Prune", true)) return;
  try { const r = await api("POST", `${P()}/images/prune`); toast(`Removed ${r.deleted} images, freed ${bytes(r.spaceReclaimed)}`); render(); } catch (e) { toast(e.message, true); }
}

function scanImage(ref) {
  runJob("POST", `${P()}/images/scan`, { ref });
}

// ---------- Volumes ----------

async function renderVolumes(main) {
  const head = el("div.head",
    el("div", el("h1", "Volumes"), el("p.muted", "Named and anonymous volumes, their size and who uses them.")),
    el("div.toolbar", el("button.ghost", { type: "button", onclick: pruneVolumes }, "Prune unused")));
  if (!isRunning()) { main.replaceChildren(head, notRunningCard()); return; }
  const body = el("div.card.table-wrap", el("p.muted", "Loading…"));
  main.replaceChildren(head, body);
  const vols = await api("GET", `${P()}/volumes`);
  if (!vols.length) { body.replaceChildren(el("p.empty", "No volumes.")); return; }
  body.replaceChildren(el("table",
    el("thead", el("tr", ["Name", "Size", "Used by", "Created", ""].map((h) => el("th", h)))),
    el("tbody", vols.map((v) => el("tr",
      el("td.mono.small", v.Name),
      el("td", v.UsageData ? bytes(v.UsageData.Size) : "–"),
      el("td", v.usedBy.length ? v.usedBy.join(", ") : el("span.pill", "unused")),
      el("td.small", v.CreatedAt ? new Date(v.CreatedAt).toLocaleString() : "–"),
      el("td.actions",
        el("button.ghost.mini", { type: "button", onclick: () => showFiles(v.Name, v.Name, "volume") }, "Browse"),
        el("button.ghost.mini.danger", { type: "button", onclick: () => removeVolume(v) }, "Remove")))))));
}

async function removeVolume(v) {
  if (!await confirmBox("Remove volume?", el("p", "The data in ", el("code", v.Name), " is deleted for good."), "Remove", true)) return;
  try { await api("DELETE", `${P()}/volumes/${encodeURIComponent(v.Name)}`); toast("Volume removed"); render(); } catch (e) { toast(e.message, true); }
}

async function pruneVolumes() {
  if (!await confirmBox("Prune unused volumes?", "Every volume no container uses is deleted, with its data.", "Prune", true)) return;
  try { const r = await api("POST", `${P()}/volumes/prune`); toast(`Removed ${r.deleted} volumes, freed ${bytes(r.spaceReclaimed)}`); render(); } catch (e) { toast(e.message, true); }
}

// ---------- Kubernetes ----------

async function renderKubernetes(main) {
  const head = el("div.head", el("div", el("h1", "Kubernetes"), el("p.muted", "k3s inside the Colima VM: version, ingress, workloads and their logs.")));
  if (!isRunning()) { main.replaceChildren(head, notRunningCard()); return; }
  main.replaceChildren(head, el("div.card", el("p.muted", "Loading…")));
  const k = await api("GET", `${P()}/kubernetes`);
  const set = k.settings || {};
  const restartNote = `Colima restarts profile ${state.profile}; running containers stop for a minute.`;

  // Settings: on or off, version, Traefik, reset. Each change restarts the VM, so each asks first.
  const toggle = el("button", { type: "button", class: k.enabled ? "ghost" : "" }, k.enabled ? "Turn off" : "Turn on");
  toggle.addEventListener("click", async () => {
    const on = !k.enabled;
    if (!await confirmBox(on ? "Turn Kubernetes on?" : "Turn Kubernetes off?", restartNote, on ? "Turn on" : "Turn off", false)) return;
    runJob("POST", `${P()}/actions/apply`, { kubernetes: on }, () => render());
  });
  const verSel = el("select", { "aria-label": "k3s version" }, el("option", { value: set.version || "" }, set.version || "colima's default"));
  const verBtn = el("button.ghost", { type: "button", disabled: true }, "Change version");
  api("GET", "/api/k3s/versions").then((vs) => {
    const opts = vs.map((v) => el("option", { value: v.version, selected: v.version === set.version },
      `${v.version}  (${v.channel === "stable" ? "stable" : v.channel})`));
    if (set.version && !vs.some((v) => v.version === set.version)) opts.unshift(el("option", { value: set.version, selected: true }, `${set.version}  (current)`));
    verSel.replaceChildren(...opts);
  }).catch(() => { /* keep the current version only */ });
  verSel.addEventListener("change", () => { verBtn.disabled = verSel.value === set.version; });
  verBtn.addEventListener("click", async () => {
    const v = verSel.value;
    const older = set.version && v.localeCompare(set.version, undefined, { numeric: true }) < 0;
    if (!await confirmBox(`Switch k3s to ${v}?`, older
      ? `${v} is older than ${set.version}. k3s cannot go back a version in place: reset Kubernetes afterwards if it fails to start. ${restartNote}`
      : `${restartNote} Workloads stay.`, "Switch", older)) return;
    runJob("POST", `${P()}/actions/apply`, { kubernetesVersion: v, ...(k.enabled ? {} : { kubernetes: true }) }, () => render());
  });
  const traefik = el("input", { type: "checkbox", checked: !!set.traefik });
  traefik.addEventListener("change", async () => {
    const on = traefik.checked;
    if (!await confirmBox(on ? "Turn on Traefik?" : "Turn off Traefik?",
      `${on ? "k3s installs the Traefik ingress controller and load balancer on ports 80 and 443." : "k3s removes Traefik; Ingress resources stop being served."} ${restartNote}`, on ? "Turn on" : "Turn off", false)) {
      traefik.checked = !on; return;
    }
    runJob("POST", `${P()}/actions/apply`, { traefik: on }, () => render());
  });
  const reset = el("button.ghost.danger", { type: "button", onclick: resetKubernetes }, "Reset Kubernetes…");
  const settings = el("section.card.k8s-card",
    el("div.vm-head",
      el("div.vm-title", el("span.vm-label", "Kubernetes"), el("h2", k.enabled ? "k3s is running" : "Off")),
      el("div.k8s-actions", el(`span.status.${k.enabled ? "on" : "off"}`, el("i"), k.enabled ? "On" : "Off"), toggle)),
    el("div.k8s-settings",
      el("div.k8s-row", el("div", el("strong", "Version"), el("p.small.muted", "A k3s release. New versions upgrade in place; going back needs a reset.")),
        el("div.k8s-ctl", verSel, verBtn)),
      el("div.k8s-row", el("div", el("strong", "Traefik ingress"), el("p.small.muted", "k3s's bundled ingress controller. Colima turns it off by default; Rancher Desktop turns it on.")),
        el("label.switch", traefik, el("span", set.traefik ? "On" : "Off"))),
      k.enabled ? el("div.k8s-row", el("div", el("strong", "Reset"), el("p.small.muted", "Deletes the cluster and creates an empty one. Containers, images and volumes outside Kubernetes stay.")),
        reset) : null));
  const parts = [settings];

  if (k.enabled) {
    const ctx = el("section.card", el("h2", "kubectl on your Mac"));
    if (k.currentContextError) {
      ctx.append(el("p", k.currentContextError + ". Colima wrote the context ", el("code", k.context), " to ~/.kube/config."), fixRow({ id: "kubectx", title: "kubectl", fix: "brew install kubectl", action: "terminal" }));
    } else if (k.currentContext !== k.context) {
      ctx.append(el("p", "kubectl points at ", el("code", k.currentContext), ", not this profile."), fixRow({ id: "kubectx", title: "kubectl context", fix: `kubectl config use-context ${k.context}`, action: "terminal" }));
    } else {
      ctx.append(el("p", el("span.pill.ok", "ok"), " kubectl uses ", el("code", k.context), "."));
    }
    parts.push(workloadsCard(), ctx,
      el("details.card", el("summary", el("strong", "Nodes and version")), el("pre.log", ((k.nodes || k.nodesError || "") + "\n" + (k.version || "")).trim())));
  }
  main.replaceChildren(head, ...parts);
}

function workloadsCard() {
  const ns = el("select", { "aria-label": "Namespace" }, el("option", { value: "" }, "All namespaces"));
  const sys = el("input", { type: "checkbox" });
  const body = el("div", el("p.muted.small", "Loading…"));
  const card = el("section.card", el("div.card-head", el("h2", "Workloads"),
    el("div.toolbar", ns, el("label.check", sys, "Show kube-system"),
      el("button.ghost.mini", { type: "button", onclick: () => load() }, "Refresh"))), body);
  let pods = [];
  const tone = (p) => p.reason === "Terminating" ? "busy"
    : p.reason ? "fail" : p.phase === "Running" && p.ready.split("/")[0] === p.ready.split("/")[1] ? "ok"
      : p.phase === "Succeeded" ? "x" : p.phase === "Failed" ? "fail" : "warn";
  const draw = () => {
    const shown = pods.filter((p) => (!ns.value || p.namespace === ns.value) && (sys.checked || ns.value === "kube-system" || p.namespace !== "kube-system"));
    if (!shown.length) { body.replaceChildren(el("p.muted", pods.length ? "Nothing here; kube-system is hidden." : "No pods yet. kubectl apply something and it shows up here.")); return; }
    body.replaceChildren(el("div.table-wrap", el("table",
      el("thead", el("tr", el("th", "Pod"), el("th", "Namespace"), el("th", "Status"), el("th", "Ready"), el("th", "Restarts"), el("th", "Age"), el("th"))),
      shortBody(shown.map((p) => el("tr",
        el("td", el("strong.mono", p.name), p.owner ? el("div.muted.small", p.owner) : null),
        el("td.small", p.namespace),
        el("td", el(`span.pill.${tone(p)}`, p.reason || p.phase)),
        el("td.small.num", p.ready),
        el("td.small.num", p.restarts ? el("span.bad-text", String(p.restarts)) : "0"),
        el("td.small", ago(p.created)),
        el("td.actions",
          el("button.ghost.mini", { type: "button", onclick: () => showPodLogs(p) }, "Logs"),
          el("button.ghost.mini.danger", { type: "button", onclick: () => deletePod(p, load) }, "Delete")))), 25))));
  };
  const load = async () => {
    try { pods = await api("GET", `${P()}/kubernetes/pods`); }
    catch (e) { body.replaceChildren(el("div.notice.bad", e.message)); return; }
    const names = [...new Set(pods.map((p) => p.namespace))].sort();
    const cur = ns.value;
    ns.replaceChildren(el("option", { value: "" }, "All namespaces"), ...names.map((n) => el("option", { value: n, selected: n === cur }, n)));
    draw();
  };
  ns.addEventListener("change", draw);
  sys.addEventListener("change", draw);
  load();
  return card;
}

function showPodLogs(p) {
  const log = el("pre.log");
  const follow = el("input", { type: "checkbox", checked: true });
  const prev = el("input", { type: "checkbox" });
  const lines = [];
  const start = () => {
    if (state.logSource) state.logSource.close();
    lines.length = 0;
    log.replaceChildren();
    const src = new EventSource(`${P()}/kubernetes/pods/${encodeURIComponent(p.namespace)}/${encodeURIComponent(p.name)}/logs?tail=500&follow=${follow.checked ? 1 : 0}&previous=${prev.checked ? 1 : 0}`);
    state.logSource = src;
    src.addEventListener("line", (e) => {
      const l = JSON.parse(e.data);
      lines.push(l.t);
      const atBottom = log.scrollTop + log.clientHeight >= log.scrollHeight - 30;
      log.append(lineNode(l, ""));
      if (log.childNodes.length > 5000) log.firstChild.remove();
      if (atBottom) log.scrollTop = log.scrollHeight;
    });
    src.addEventListener("done", () => src.close());
    src.addEventListener("failed", (e) => { src.close(); log.append(el("span.err", JSON.parse(e.data) + "\n")); });
  };
  follow.addEventListener("change", start);
  prev.addEventListener("change", start);
  openDrawer(`Logs: ${p.namespace}/${p.name}`,
    el("div.toolbar", el("label.check", follow, "Follow"), el("label.check", prev, "Previous run (after a crash)"),
      el("button.ghost.mini", { type: "button", onclick: () => download(`${p.name}.log`, lines.join("\n")) }, "Download")),
    log);
  start();
}

async function deletePod(p, after) {
  if (!await confirmBox(`Delete pod ${p.name}?`, p.owner ? `${p.owner} starts a new one straight away, so this is a restart.` : "Nothing owns this pod, so it is gone for good.", "Delete", true)) return;
  try { await api("DELETE", `${P()}/kubernetes/pods/${encodeURIComponent(p.namespace)}/${encodeURIComponent(p.name)}`); toast(`Deleting ${p.name}`); setTimeout(after, 800); }
  catch (e) { toast(e.message, true); }
}

// ---------- Snapshots ----------

async function renderSnapshots(main) {
  const p = currentProfile();
  const running = isRunning();
  const head = el("div.head", el("div", el("h1", "Snapshots"),
    el("p.muted", `Save profile ${state.profile} as it is now (VM, images, containers, volumes and settings) and go back to it later.`)));
  if (!p) { main.replaceChildren(head, notRunningCard()); return; }
  const name = el("input", { type: "text", placeholder: "Name, e.g. Before upgrading Postgres", maxlength: 80, "aria-label": "Snapshot name" });
  const note = el("input", { type: "text", placeholder: "Description (optional)", maxlength: 500, "aria-label": "Snapshot description" });
  const take = el("button", { type: "button", disabled: running }, "Save snapshot");
  take.addEventListener("click", () => runJob("POST", `${P()}/snapshots`, { name: name.value, note: note.value }, () => render()));
  name.addEventListener("keydown", (e) => { if (e.key === "Enter" && !take.disabled) take.click(); });
  const stopBtn = el("button.ghost", { type: "button", onclick: () => runJob("POST", `${P()}/actions/stop`, undefined, render) }, `Stop ${state.profile}`);
  const save = el("section.card",
    el("h2", "New snapshot"),
    running ? el("div.notice", el("p", "Snapshots are taken and restored with the VM stopped, so the disks are consistent. Stopping takes a few seconds."), stopBtn) : null,
    el("div.snap-new", name, note, take),
    el("p.small.muted", "Snapshots are APFS clones: saving takes a second, and they only use disk space for what changes afterwards. Kept in ~/Library/Application Support/colima-dashboard/snapshots."));
  const listCard = el("section.card", el("h2", "Saved"), el("p.muted.small", "Loading…"));
  main.replaceChildren(head, save, listCard);
  let list;
  try { list = await api("GET", `${P()}/snapshots`); } catch (e) { listCard.replaceChildren(el("h2", "Saved"), el("div.notice.bad", e.message)); return; }
  if (!list.length) { listCard.replaceChildren(el("h2", "Saved"), el("p.muted", "No snapshots yet.")); return; }
  const restore = async (sn) => {
    if (!await confirmBox(`Restore "${sn.name}"?`, `Profile ${state.profile} goes back to how it was on ${new Date(sn.created).toLocaleString()}. Anything since then is replaced; the current state is saved first as an automatic snapshot, so you can undo this.`, "Restore", true)) return;
    runJob("POST", `${P()}/snapshots/${encodeURIComponent(sn.id)}/restore`, undefined, () => render());
  };
  const del = async (sn) => {
    if (!await confirmBox(`Delete "${sn.name}"?`, "The snapshot is removed. The profile is not touched.", "Delete", true)) return;
    try { await api("DELETE", `${P()}/snapshots/${encodeURIComponent(sn.id)}`); toast("Snapshot deleted"); render(); } catch (e) { toast(e.message, true); }
  };
  listCard.replaceChildren(el("h2", "Saved"), el("div.table-wrap", el("table",
    el("thead", el("tr", el("th", "Name"), el("th", "Saved"), el("th"))),
    el("tbody", list.map((sn) => el("tr",
      el("td", el("strong", sn.name), sn.auto ? [" ", el("span.pill.x", "automatic")] : null, sn.note ? el("div.muted.small", sn.note) : null),
      el("td.small", new Date(sn.created).toLocaleString(undefined, { dateStyle: "medium", timeStyle: "short" }), el("div.muted.small", ago(Date.parse(sn.created) / 1000))),
      el("td.actions",
        el("button.ghost.mini", { type: "button", disabled: running, title: running ? "Stop the profile first" : "", onclick: () => restore(sn) }, "Restore"),
        el("button.ghost.mini.danger", { type: "button", onclick: () => del(sn) }, "Delete"))))))));
}

// ---------- Preferences ----------

async function renderPreferences(main) {
  const p = currentProfile();
  const head = el("div.head",
    el("div", el("h1", "Preferences"), el("p.muted", `Virtual machine and container engine settings for profile ${state.profile}.`)));
  let st = null;
  if (isRunning()) { try { st = (await api("GET", `${P()}/status`)).status; } catch { /* shown as unknown */ } }
  const num = (label, value, min, max, help) => {
    const input = el("input", { type: "number", min, max, value: value || "", required: true });
    return [el("label.field", el("span", label), input, help ? el("span.small.muted", help) : null), input];
  };
  const sel = (label, value, options) => {
    const s = el("select", options.map((o) => el("option", { value: o, selected: o === value }, o)));
    return [el("label.field", el("span", label), s), s];
  };
  const memGiB = p?.memory ? Math.round(p.memory / 1073741824) : 2;
  const diskGiB = p?.disk ? Math.round(p.disk / 1073741824) : 100;
  const [cpuF, cpu] = num("CPUs", p?.cpus || 2, 1, 64);
  const [memF, mem] = num("Memory (GiB)", memGiB, 1, 512);
  const [diskF, disk] = num("Disk (GiB)", diskGiB, diskGiB, 4096, "Can only grow.");
  const [rtF, rt] = sel("Container runtime", p?.runtime || "docker", ["docker", "containerd"]);
  const vmNow = st?.driver?.includes("Virtualization") ? "vz" : st?.driver?.toLowerCase().includes("qemu") ? "qemu" : "vz";
  const [vmF, vm] = sel("VM type", vmNow, ["vz", "qemu"]);
  const [mtF, mt] = sel("Mount type", st?.mount_type || "virtiofs", ["virtiofs", "sshfs", "9p"]);
  const rosetta = el("input", { type: "checkbox" });
  const k8s = el("input", { type: "checkbox", checked: !!st?.kubernetes });
  const form = el("div.card",
    el("div.form", cpuF, memF, diskF, rtF, vmF, mtF),
    el("h3", "Options"),
    el("label.check", rosetta, "Rosetta for amd64 images (vz only)"),
    el("label.check", k8s, "Kubernetes (k3s)"),
    el("p.small.muted", "Changing the runtime or VM type of an existing profile needs a fresh VM: delete the profile, then start it with the new settings."));
  const apply = el("button", { type: "button" }, isRunning() ? "Apply and restart" : "Save and start");
  apply.addEventListener("click", async () => {
    const opts = {
      cpus: +cpu.value, memoryGiB: +mem.value, diskGiB: +disk.value,
      runtime: rt.value, vmType: vm.value, mountType: mt.value,
      rosetta: rosetta.checked, kubernetes: k8s.checked,
    };
    if (opts.diskGiB < diskGiB) { toast(`Disk cannot shrink below ${diskGiB} GiB`, true); return; }
    if (!await confirmBox("Apply settings?", isRunning() ? `Colima stops profile ${state.profile} and starts it again with these settings. Running containers stop.` : `Colima starts profile ${state.profile} with these settings.`, "Apply", false)) return;
    runJob("POST", `${P()}/actions/${isRunning() ? "apply" : "start"}`, opts, () => render());
  });
  const newProf = el("input", { placeholder: "dev", pattern: "[A-Za-z0-9_-]+", "aria-label": "New profile name" });
  const create = el("button.ghost", { type: "button" }, "Create and start");
  create.addEventListener("click", () => {
    const n = newProf.value.trim();
    if (!/^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$/.test(n)) { toast("Use letters, digits, - and _ only", true); return; }
    runJob("POST", `/api/profiles/${encodeURIComponent(n)}/actions/start`, {}, () => { state.profile = n; safeSet("profile", n); refreshOverview().then(render); });
  });
  const profiles = el("div.card", el("h2", "New profile"), el("p.muted", "A profile is a separate VM with its own engine, images and settings."), el("div.toolbar", newProf, create));
  main.replaceChildren(head, form, el("div.toolbar", apply), el("p"), profiles, appCard(), alertsCard(),
    el("p.small.muted", "To delete this profile, use Factory reset under Troubleshooting."));
}

// The dashboard itself: start at login and the colour theme (this browser only).
function appCard() {
  const login = el("input", { type: "checkbox", disabled: true });
  const loginNote = el("span.muted.small", "");
  api("GET", "/api/login-item").then((l) => {
    login.checked = l.enabled; login.disabled = !l.available;
    if (!l.available) loginNote.textContent = " (not available in this build)";
  }).catch(() => {});
  login.addEventListener("change", async () => {
    try { await api("POST", "/api/login-item", { enabled: login.checked }); toast(login.checked ? "Colima Dashboard starts at your next login" : "Colima Dashboard no longer starts at login"); }
    catch (e) { login.checked = !login.checked; toast(e.message, true); }
  });
  const theme = el("select", { "aria-label": "Theme" },
    [["system", "Match macOS"], ["light", "Light"], ["dark", "Dark"]].map(([v, t]) => el("option", { value: v }, t)));
  theme.value = safeGet("theme") || "system";
  theme.addEventListener("change", () => { safeSet("theme", theme.value); applyTheme(); });
  return el("section.card", el("h2", "Colima Dashboard"),
    el("label.check", login, "Start Colima Dashboard when I log in", loginNote),
    el("div.field-row", el("span", "Theme"), theme));
}

function applyTheme() {
  const t = safeGet("theme");
  if (t === "light" || t === "dark") document.documentElement.dataset.theme = t;
  else delete document.documentElement.dataset.theme;
  drawThemeBtn();
}

// The status bar's sun/moon button flips between light and dark. "Follow the system" stays in Preferences.
const darkQuery = window.matchMedia("(prefers-color-scheme: dark)");
function isDark() { const t = document.documentElement.dataset.theme; return t ? t === "dark" : darkQuery.matches; }
function drawThemeBtn() {
  const b = document.getElementById("sb-theme");
  if (!b) return;
  const dark = isDark();
  const label = dark ? "Switch to light mode" : "Switch to dark mode";
  b.title = label; b.setAttribute("aria-label", label);
  b.innerHTML = dark
    ? '<svg viewBox="0 0 24 24" width="16" height="16" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round"><circle cx="12" cy="12" r="4.5"/><path d="M12 2v2M12 20v2M2 12h2M20 12h2M4.9 4.9l1.4 1.4M17.7 17.7l1.4 1.4M4.9 19.1l1.4-1.4M17.7 6.3l1.4-1.4"/></svg>'
    : '<svg viewBox="0 0 24 24" width="16" height="16" fill="none" stroke="currentColor" stroke-width="2" stroke-linejoin="round"><path d="M20.5 14.5A8.5 8.5 0 0 1 9.5 3.5a8.5 8.5 0 1 0 11 11z"/></svg>';
}
document.getElementById("sb-theme")?.addEventListener("click", () => {
  safeSet("theme", isDark() ? "light" : "dark");
  applyTheme();
  const sel = document.querySelector('select[aria-label="Theme"]');
  if (sel) sel.value = safeGet("theme");
});
darkQuery.addEventListener("change", drawThemeBtn);
applyTheme();

// Notifications and the menu bar icon, plus the latest alerts.
function alertsCard() {
  const card = el("section.card", el("h2", "Alerts and menu bar"), el("p.muted.small", "Loading…"));
  api("GET", "/api/alerts").then((a) => {
    const notify = el("input", { type: "checkbox", checked: a.settings.notify });
    const menu = el("input", { type: "checkbox", checked: a.settings.menuBar, disabled: !a.menuBarAvailable });
    const save = async () => {
      try { await api("POST", "/api/alerts/settings", { notify: notify.checked, menuBar: menu.checked }); toast("Saved"); }
      catch (e) { toast(e.message, true); }
    };
    notify.addEventListener("change", save);
    menu.addEventListener("change", save);
    const list = a.alerts.length
      ? el("ul.alert-list", a.alerts.slice(0, 10).map((x) => el("li",
        el("span.sev.fail", x.kind === "build" ? "Build" : "Run"),
        el("div", el("strong", x.title), el("div.small.muted", `${x.body} · ${x.profile} · ${ago(Date.parse(x.time) / 1000)}`)))))
      : el("p.small.muted", "No alerts since the dashboard started.");
    card.replaceChildren(el("h2", "Alerts and menu bar"),
      el("label.check", notify, "Notify me when a test container or a build fails, or a container runs out of memory"),
      el("label.check", menu, a.menuBarAvailable ? "Show Colima in the menu bar (state, start and stop, recent alerts)" : "Menu bar icon (in release builds only; this is a development build)"),
      el("h3", "Recent alerts"), list);
    if (a.unread) api("POST", "/api/alerts/read").catch(() => {});
  }).catch((e) => card.replaceChildren(el("h2", "Alerts and menu bar"), el("div.notice.bad", e.message)));
  return card;
}

async function deleteProfile() {
  const n = state.profile;
  if (!await confirmBox(`Delete profile ${n}?`, "This cannot be undone. Every container, image and volume in it is lost.", "Delete", true, n)) return;
  runJob("POST", `${P()}/actions/delete`, undefined, () => { state.profile = null; refreshOverview().then(render); });
}

// ---------- Troubleshooting ----------

async function renderTroubleshooting(main) {
  const head = el("div.head",
    el("div", el("h1", "Troubleshooting"), el("p.muted", `Logs and resets for profile ${state.profile}. Checks with fixes are under Diagnostics.`)));
  const logs = el("div.card", el("h2", "Logs"),
    el("p.muted", "Colima's and the VM's own log files: start-up, the host agent and the VM console."),
    el("button.ghost", { type: "button", onclick: () => showVMLogs() }, "Show logs"));
  const cards = [logs];
  const st = currentProfile();
  if (isRunning() && st?.kubernetes) {
    cards.push(el("div.card", el("h2", "Reset Kubernetes"),
      el("p.muted", "Deletes the k3s cluster and creates an empty one. Workloads and Kubernetes data go; containers, images and volumes outside Kubernetes stay."),
      el("button.ghost", { type: "button", onclick: resetKubernetes }, "Reset Kubernetes…")));
  }
  cards.push(el("div.card", el("h2", "Clean up downloads"),
    el("p.muted", "Remove Colima's cached VM images and downloads. Profiles are not touched."),
    el("button.ghost", { type: "button", onclick: pruneCache }, "Prune cached downloads")));
  cards.push(el("div.card", el("h2", "Factory reset"),
    el("p.muted", `Deletes profile ${state.profile}: the VM with every container, image and volume in it, and its settings. Start it again from General for a clean VM.`),
    el("button.danger", { type: "button", onclick: deleteProfile }, `Delete ${state.profile}…`)));
  main.replaceChildren(head, ...cards);
}

async function resetKubernetes() {
  if (!await confirmBox("Reset Kubernetes?", `Colima deletes the k3s cluster in ${state.profile} and creates an empty one.`, "Reset", true)) return;
  runJob("POST", `${P()}/actions/kubernetes-reset`, undefined, () => render());
}

// ---------- Diagnostics ----------

async function renderDiagnostics(main) {
  const head = el("div.head",
    el("div", el("h1", "Diagnostics"), el("p.muted", "Checks for the usual Colima problems, each with its fix.")),
    el("div.toolbar", el("button.ghost", { type: "button", onclick: () => render() }, "Run again")));
  const checks = el("div.card", el("p.muted", "Checking…"));
  main.replaceChildren(head, checks);
  const list = await api("GET", `/api/diagnostics?profile=${encodeURIComponent(state.profile)}`);
  setDiagBadge(list);
  const showMuted = el("input", { type: "checkbox", checked: safeGet("showMuted") === "1" });
  const paint = () => {
    const muted = mutedSet();
    const visible = list.filter((c) => showMuted.checked || !muted.has(muteKey(c)));
    const hidden = list.length - visible.length;
    checks.replaceChildren(
      el("div.card-head", el("span.muted.small", hidden ? `${hidden} muted check${hidden === 1 ? "" : "s"} hidden` : `${list.length} checks`),
        el("label.check.small", showMuted, "Show muted")),
      el("table", el("tbody", visible.map((c) => {
        const problem = c.state === "fail" || c.state === "warn";
        const mute = problem ? el("button.ghost.mini", { type: "button" }, muted.has(muteKey(c)) ? "Unmute" : "Mute") : null;
        if (mute) mute.addEventListener("click", () => { setMuted(c, !isMuted(c)); setDiagBadge(list); paint(); });
        return el(muted.has(muteKey(c)) ? "tr.muted-row" : "tr",
          el("td", el(`span.pill.${c.state}`, c.state)),
          el("td", el("strong", c.title), el("div.muted.small", c.detail), fixRow(c)),
          el("td.actions", mute));
      }))));
  };
  showMuted.addEventListener("change", () => { safeSet("showMuted", showMuted.checked ? "1" : "0"); paint(); });
  paint();
}

function setDiagBadge(list) {
  const b = $("#diagbadge");
  const fails = list.filter((c) => c.state === "fail" && !isMuted(c)).length;
  const warns = list.filter((c) => c.state === "warn" && !isMuted(c)).length;
  const n = fails + warns;
  b.hidden = n === 0;
  b.textContent = String(n);
  b.className = fails ? "badge" : "badge warn";
  b.title = `${fails} failing, ${warns} warnings`;
}

async function refreshDiagBadge() {
  try { setDiagBadge(await api("GET", `/api/diagnostics?profile=${encodeURIComponent(state.profile)}`)); } catch { /* badge stays as it was */ }
}

// ---------- Port Forwarding ----------

async function renderPorts(main) {
  const head = el("div.head",
    el("div", el("h1", "Port Forwarding"), el("p.muted", "What you can reach on localhost. Colima forwards every published container port and Kubernetes LoadBalancer port by itself; there is nothing to switch on.")));
  if (!isRunning()) { main.replaceChildren(head, notRunningCard()); return; }
  const cCard = el("div.card", el("h2", "Containers"), el("p.muted", "Loading…"));
  const kCard = currentProfile()?.kubernetes ? el("div.card", el("h2", "Kubernetes services"), el("p.muted", "Loading…")) : null;
  main.replaceChildren(head, cCard, ...(kCard ? [kCard] : []));
  const open = (port) => el("a", { href: `http://localhost:${port}`, target: "_blank", rel: "noopener noreferrer" }, `localhost:${port}`);
  try {
    const cs = await api("GET", `${P()}/containers`);
    const rows = [];
    for (const c of cs) {
      if (c.State !== "running") continue;
      const seen = new Set();
      for (const p of c.Ports || []) {
        if (!p.PublicPort || seen.has(p.PublicPort + p.Type)) continue;
        seen.add(p.PublicPort + p.Type);
        rows.push(el("tr",
          el("td", el("strong", (c.Names[0] || "").replace(/^\//, ""))),
          el("td", p.Type === "tcp" ? open(p.PublicPort) : el("span.mono", `localhost:${p.PublicPort}`)),
          el("td.mono", `${p.PrivatePort}/${p.Type}`),
          el("td.small.muted", p.IP && p.IP !== "0.0.0.0" && p.IP !== "::" ? `bound to ${p.IP}` : "all addresses")));
      }
    }
    cCard.replaceChildren(el("h2", "Containers"), rows.length
      ? el("table", el("thead", el("tr", ["Container", "On your Mac", "In the container", ""].map((h) => el("th", h)))), el("tbody", rows))
      : el("p.muted", "No running container publishes a port. Publish one with ", el("code", "docker run -p 8080:80 …"), "."));
  } catch (e) {
    cCard.replaceChildren(el("h2", "Containers"), el("div.notice.bad", e.message));
  }
  if (!kCard) return;
  try {
    const svcs = await api("GET", `${P()}/kubernetes/services`);
    const rows = [];
    for (const s of svcs) {
      for (const p of s.ports || []) {
        const local = s.type === "LoadBalancer" ? p.port : s.type === "NodePort" ? p.nodePort : 0;
        rows.push(el("tr",
          el("td", el("strong", s.name), el("div.muted.small", s.namespace)),
          el("td", s.type),
          el("td.mono", `${p.port}/${(p.protocol || "TCP").toLowerCase()}`),
          el("td", local ? open(local) : el("span.muted.small", "inside the cluster only"))));
      }
    }
    kCard.replaceChildren(el("h2", "Kubernetes services"), rows.length
      ? el("table", el("thead", el("tr", ["Service", "Type", "Port", "On your Mac"].map((h) => el("th", h)))), el("tbody", rows))
      : el("p.muted", "No services."),
      el("p.small.muted", "ClusterIP services are reachable inside the cluster only; reach one from the Mac with ", el("code", "kubectl port-forward"), "."));
  } catch (e) {
    kCard.replaceChildren(el("h2", "Kubernetes services"), el("div.notice.bad", e.message));
  }
}

// A whole log file, formatted line by line (the last 2000 lines; Download has all of it).
function logBlock(text) {
  const pre = el("pre.log");
  const lines = (text || "").split("\n");
  if (lines.length && lines[lines.length - 1] === "") lines.pop();
  if (!lines.length) { pre.textContent = "(empty)"; return pre; }
  const shown = lines.slice(-2000);
  if (shown.length < lines.length) pre.append(el("div.muted.small", `… ${lines.length - shown.length} earlier lines; Download for all of them`));
  pre.append(...shown.map((l) => logfmt.line(l)));
  queueMicrotask(() => { pre.scrollTop = pre.scrollHeight; });
  return pre;
}

async function showVMLogs() {
  openDrawer(`VM logs: ${state.profile}`, el("p.muted", "Loading…"));
  try {
    const logs = await api("GET", `${P()}/vmlogs`);
    const names = Object.keys(logs);
    if (!names.length) { $("#drawerbody").replaceChildren(el("p.muted", "No log files for this profile yet.")); return; }
    $("#drawerbody").replaceChildren(...names.flatMap((n) => [
      el("div.toolbar", el("h2", n), el("button.ghost.mini", { type: "button", onclick: () => download(`${state.profile}-${n}`, logs[n]) }, "Download")),
      logBlock(logs[n]),
    ]));
  } catch (e) {
    $("#drawerbody").replaceChildren(el("div.notice.bad", e.message));
  }
}

async function pruneCache() {
  if (!await confirmBox("Prune cached downloads?", "Colima deletes its cached VM images. The next new profile downloads them again.", "Prune", false)) return;
  runJob("POST", "/api/colima/prune");
}

const tabs = {
  general: renderGeneral,
  containers: renderContainers,
  images: renderImages,
  volumes: renderVolumes,
  kubernetes: renderKubernetes,
  snapshots: renderSnapshots,
  preferences: renderPreferences,
  troubleshooting: renderTroubleshooting,
  diagnostics: renderDiagnostics,
  ports: renderPorts,
};

// Sidebar icons: a tinted tile per section.
const NAV_ICONS = {
  general: ["#2a78d6", "M3 12h4l3 8 4-16 3 8h4"],
  monitor: ["#7c3aed", "M3 3v18h18M7 15l4-4 3 3 5-6"],
  containers: ["#0f766e", "M3 7l9-4 9 4v10l-9 4-9-4zM3 7l9 4 9-4M12 11v10"],
  images: ["#c2410c", "M4 6h16v12H4zM4 14l5-4 4 3 3-2 4 3"],
  volumes: ["#b45309", "M4 6c0-1.7 3.6-3 8-3s8 1.3 8 3-3.6 3-8 3-8-1.3-8-3zM4 6v12c0 1.7 3.6 3 8 3s8-1.3 8-3V6M4 12c0 1.7 3.6 3 8 3s8-1.3 8-3"],
  kubernetes: ["#1d4ed8", "M12 2l8 4.5v9L12 20l-8-4.5v-9zM12 8v4m0 0l3.5 2M12 12l-3.5 2"],
  preferences: ["#475569", "M4 6h10M18 6h2M4 12h4M12 12h8M4 18h12M20 18h0M14 4v4M8 10v4M16 16v4"],
  troubleshooting: ["#be123c", "M14.7 6.3a4 4 0 0 0-5.4 5.4L3 18l3 3 6.3-6.3a4 4 0 0 0 5.4-5.4l-2.5 2.5-2.4-.6-.6-2.4z"],
  diagnostics: ["#b91c1c", "M12 3l9 16H3zM12 10v4M12 17v.5"],
  ports: ["#0e7490", "M4 8h12l-3-3M20 16H8l3 3"],
  runs: ["#15803d", "M4 6h9M4 12h9M4 18h5M14 16l3 3 5-6"],
  snapshots: ["#0369a1", "M4 7h3l2-3h6l2 3h3v12H4zM12 10a3.5 3.5 0 1 0 0 7 3.5 3.5 0 0 0 0-7z"],
  builds: ["#9333ea", "M12 3l9 5-9 5-9-5zM3 13l9 5 9-5M3 17.5l9 5 9-5"],
};
function decorateNav() {
  for (const a of document.querySelectorAll("#nav a")) {
    const [color, d] = NAV_ICONS[a.dataset.tab] || [];
    if (!d || a.querySelector(".ico")) continue;
    const ico = el("span.ico");
    ico.style.background = color + "22";
    ico.style.color = color;
    const s = document.createElementNS("http://www.w3.org/2000/svg", "svg");
    s.setAttribute("viewBox", "0 0 24 24");
    const p = document.createElementNS("http://www.w3.org/2000/svg", "path");
    p.setAttribute("d", d);
    s.append(p);
    ico.append(s);
    a.prepend(ico);
  }
}


// ---------- Progress of a start: the status bar and, on General, an animation ----------
// The animation runs only while a start (or restart, or apply) runs, never on an idle page.

const START_STEPS = [
  { key: "prep", label: "Preparing", re: /starting colima|creating and starting|using the existing instance/i },
  { key: "vm", label: "Booting the virtual machine", re: /\(\s*\d+\/\d+\)|starting vz|waiting for the|hostagent|guest agent/i },
  { key: "engine", label: "Starting the container engine", re: /context=(docker|containerd|incus)/i },
  { key: "k8s", label: "Starting Kubernetes", re: /context=kubernetes/i },
  { key: "ready", label: "Ready", re: /msg=done\b|msg="done"/i },
];
const tracker = { job: null, kind: "", profile: "", step: -1, sub: 0, k8s: false, sawStart: false, done: false, ok: true, started: 0, showUntil: 0, tick: null };

function jobKind(title) {
  const m = /^colima (start|restart|apply|stop|delete|kubernetes-reset)\b/.exec(title || "");
  return m ? m[1] : "other";
}
const startLike = (k) => k === "start" || k === "restart" || k === "apply";

function trackBegin(job) {
  Object.assign(tracker, {
    job, kind: jobKind(job.title), profile: job.profile || "", step: -1, sub: 0,
    k8s: !!(state.overview?.profiles || []).find((p) => p.name === job.profile)?.kubernetes,
    sawStart: false, done: false, ok: true, started: Date.parse(job.started) || Date.now(), showUntil: 0,
  });
  clearInterval(tracker.tick);
  tracker.tick = setInterval(paintProgress, 1000);
  paintProgress();
  if (startLike(tracker.kind) && state.tab === "general" && tracker.profile === state.profile && !$("#boot")) render();
}

function trackLine(job, line) {
  if (tracker.job !== job || !startLike(tracker.kind)) return;
  // restart and apply stop the VM first; that part ends with its own "done".
  if (/starting colima|creating and starting/i.test(line)) tracker.sawStart = true;
  if (tracker.kind !== "start" && !tracker.sawStart) return;
  START_STEPS.forEach((s, i) => { if (s.re.test(line) && i > tracker.step) tracker.step = i; });
  if (/context=kubernetes/i.test(line)) tracker.k8s = true;
  const m = /\(\s*(\d+)\/(\d+)\)/.exec(line);
  if (m) tracker.sub = Math.max(tracker.sub, +m[1] / +m[2]);
  paintProgress();
}

function trackEnd(job, ok) {
  if (tracker.job !== job) return;
  tracker.done = true;
  tracker.ok = ok;
  if (ok && startLike(tracker.kind)) tracker.step = START_STEPS.length - 1;
  clearInterval(tracker.tick);
  tracker.showUntil = Date.now() + 2500;
  paintProgress();
  setTimeout(() => { if (tracker.job === job) { tracker.job = null; paintProgress(); if ($("#boot")) render(); } }, 2600);
}

function bootVisible() {
  return !!tracker.job && startLike(tracker.kind) && tracker.profile === state.profile && (!tracker.done || Date.now() < tracker.showUntil);
}

function steps() { return START_STEPS.filter((s) => s.key !== "k8s" || tracker.k8s); }

function progressPct() {
  if (tracker.done) return 100;
  if (!startLike(tracker.kind)) return null;
  const key = tracker.step >= 0 ? START_STEPS[tracker.step].key : "";
  const k8s = tracker.k8s;
  switch (key) {
    case "prep": return 8;
    case "vm": return 10 + 50 * tracker.sub;
    case "engine": return k8s ? 70 : 85;
    case "k8s": return 90;
    case "ready": return 100;
    default: return 3;
  }
}

function elapsed() {
  const s = Math.max(0, Math.round(((tracker.done ? Date.now() : Date.now()) - tracker.started) / 1000));
  return s < 60 ? `${s}s` : `${Math.floor(s / 60)}m ${String(s % 60).padStart(2, "0")}s`;
}

const KIND_TEXT = { start: "Starting", restart: "Restarting", apply: "Applying settings to", stop: "Stopping", delete: "Deleting", "kubernetes-reset": "Resetting Kubernetes in" };

function stepLabel() {
  if (tracker.done) return tracker.ok ? "Done" : "Failed";
  if (startLike(tracker.kind)) {
    if (tracker.kind !== "start" && !tracker.sawStart) return "Stopping the virtual machine";
    return tracker.step >= 0 ? START_STEPS[tracker.step].label : "Preparing";
  }
  return tracker.job?.title || "";
}

function paintProgress() {
  const sb = $("#sb-job");
  if (!tracker.job) { sb.hidden = true; return; }
  sb.hidden = false;
  $("#sb-jobtitle").textContent = stepLabel();
  const pct = progressPct();
  const bar = $("#sb-jobbar");
  bar.style.width = `${pct === null ? 100 : pct}%`;
  bar.style.opacity = pct === null ? ".55" : "1";
  $("#sb-jobtime").textContent = elapsed();
  const boot = $("#boot");
  if (!boot) return;
  boot.classList.toggle("done", tracker.done);
  $("#boottitle").textContent = tracker.done
    ? (tracker.ok ? `${tracker.profile} is running` : `${tracker.profile} did not start`)
    : `${KIND_TEXT[tracker.kind] || "Starting"} ${tracker.profile}`;
  const list = steps();
  const cur = tracker.step >= 0 ? list.indexOf(START_STEPS[tracker.step]) : -1;
  $("#bootsteps").replaceChildren(...list.map((s, i) => el(`li${i < cur || (tracker.done && tracker.ok) ? ".done" : i === cur || (cur < 0 && i === 0) ? ".now" : ""}`, el("span.tick"), s.label)));
  $("#bootbar").style.width = `${pct ?? 0}%`;
  $("#boottime").textContent = elapsed();
}

function bootCard() {
  const svg = document.createElementNS("http://www.w3.org/2000/svg", "svg");
  svg.setAttribute("viewBox", "0 0 200 150");
  svg.setAttribute("aria-hidden", "true");
  const add = (tag, attrs) => {
    const n = document.createElementNS("http://www.w3.org/2000/svg", tag);
    for (const [k, v] of Object.entries(attrs)) n.setAttribute(k, v);
    svg.append(n);
    return n;
  };
  add("rect", { class: "vm", x: 20, y: 20, width: 160, height: 115, rx: 14 });
  add("rect", { class: "vm-top", x: 20, y: 20, width: 160, height: 22, rx: 14 });
  add("circle", { class: "led", cx: 36, cy: 31, r: 4 });
  add("circle", { class: "led", cx: 50, cy: 31, r: 4 });
  add("line", { class: "scan", x1: 30, y1: 52, x2: 170, y2: 52 });
  add("rect", { class: "box b1", x: 38, y: 90, width: 34, height: 30, rx: 5 });
  add("rect", { class: "box b2", x: 83, y: 90, width: 34, height: 30, rx: 5 });
  add("rect", { class: "box b3", x: 128, y: 90, width: 34, height: 30, rx: 5 });
  add("rect", { class: "box b4", x: 83, y: 56, width: 34, height: 30, rx: 5 });
  const card = el("section.card.boot", { id: "boot", "aria-live": "polite" },
    svg,
    el("div",
      el("h2", { id: "boottitle" }),
      el("p.boot-sub", "Colima is bringing up the virtual machine. The first start downloads an image and takes longest."),
      el("ol.steps", { id: "bootsteps" }),
      el("div.boot-foot", el("span.sb-progress", el("span", { id: "bootbar" })), el("span.mono", { id: "boottime" }),
        el("button.ghost.mini", { type: "button", onclick: () => { $("#jobpanel").hidden = false; } }, "Show output"))));
  queueMicrotask(paintProgress);
  return card;
}

$("#sb-job").addEventListener("click", () => { $("#jobpanel").hidden = false; $("#jobpanel").classList.remove("min"); });

// ---------- Status bar ----------

async function refreshStatusInfo() {
  const prof = state.profile;
  if (!prof || !isRunning()) { state.sbInfo = { profile: prof }; paintStatusBar(); return; }
  try {
    const st = await api("GET", `${P()}/status`);
    const rt = st.status?.runtime === "docker" ? "moby" : st.status?.runtime;
    const info = { profile: prof, engine: st.engine ? `${rt} ${st.engine.Version}` : rt, k8s: st.status?.kubernetes };
    if (info.k8s) {
      const prev = state.sbInfo?.profile === prof ? state.sbInfo.k8sVersion : null;
      info.k8sVersion = prev;
      if (!prev) {
        api("GET", `${P()}/kubernetes`).then((k) => {
          const m = /Server Version:\s*v?([\w.+-]+)/.exec(k.version || "");
          if (m && state.sbInfo?.profile === prof) { state.sbInfo.k8sVersion = m[1].replace(/\+.*/, ""); paintStatusBar(); }
        }).catch(() => {});
      }
    }
    state.sbInfo = info;
  } catch { state.sbInfo = { profile: prof }; }
  paintStatusBar();
}

function paintStatusBar() {
  // Same items as Rancher Desktop's status bar, label in bold, value plain.
  const ov = state.overview || {};
  const item = (id, label, ...value) => $(id).replaceChildren(el("b", `${label}:`), " ", ...value);
  item("#sb-version", "Version", ov.version || "–", ...(ov.colimaVersion ? [el("span.muted", ` (Colima ${ov.colimaVersion})`)] : []));
  const online = navigator.onLine && !/could not reach/i.test(state.updates?.checkError || "");
  item("#sb-net", "Network status", online ? "online" : "offline", el(`span.dot${online ? "" : ".off"}`));
  const i = state.sbInfo || {};
  const running = isRunning();
  item("#sb-k8s", "Kubernetes", !running ? "–" : i.k8s ? (i.k8sVersion || "on") : "off");
  item("#sb-engine", "CE", running ? (i.engine || "…") : "stopped");
  const u = state.updates;
  const btn = $("#sb-update");
  const avail = u && ((u.dashboard?.newer && u.dashboard?.canUpdate) || u.colima?.newer);
  btn.hidden = !avail;
  if (avail) btn.textContent = u.dashboard?.phase === "installed" ? "Restarting into the update…" : "Update available";
}
$("#sb-update").addEventListener("click", () => { location.hash = "#general"; render(); });

// ---------- Version checks and updates ----------

async function refreshUpdates(force) {
  try {
    state.updates = await api(force ? "POST" : "GET", force ? "/api/updates/check" : "/api/updates");
  } catch (e) {
    state.updates = { checkError: e.message };
  }
  paintStatusBar();
  const w = $("#welcome");
  if (w) w.replaceWith(welcomeCard());
}

function welcomeCard() {
  const u = state.updates;
  const ov = state.overview || {};
  const auto = el("input", { type: "checkbox", checked: !!u?.auto, disabled: !u || u.checkError && !u.dashboard });
  auto.addEventListener("change", async () => {
    try { state.updates = await api("POST", "/api/updates/settings", { auto: auto.checked }); toast(auto.checked ? "Updates are automatic" : "Automatic updates off"); }
    catch (e) { auto.checked = !auto.checked; toast(e.message, true); }
  });
  // One row per thing that can update: a status pill, one sentence, and its actions on the right.
  const row = (pill, tone, text, ...acts) => el("div.upd", el(`span.pill.${tone}`, pill), el("span.upd-text", text),
    acts.filter(Boolean).length ? el("span.upd-acts", acts.filter(Boolean)) : null);
  const link = (href, label) => el("a.small", { href, target: "_blank", rel: "noopener noreferrer" }, label);
  const rows = [];
  if (!u) rows.push(el("div.upd", el("span.muted.small", "Checking for updates…")));
  else {
    const d = u.dashboard || {};
    if (d.phase === "downloading") rows.push(row("updating", "busy", `Downloading Colima Dashboard ${d.latest?.version}…`));
    else if (d.phase === "installed") rows.push(row("installed", "ok", `Version ${d.latest?.version} is installed; the dashboard restarts when no task is running.`));
    else if (d.phase === "failed") rows.push(row("failed", "fail", `Update failed: ${d.error}`));
    else if (d.newer && d.canUpdate) rows.push(row("new", "warn", `Colima Dashboard ${d.latest.version} is out.`,
      link(d.latest.url, "What changed"), el("button.mini", { type: "button", onclick: installDashboard }, `Update to ${d.latest.version}`)));
    else if (d.newer) rows.push(row("new", "warn", `Colima Dashboard ${d.latest.version} is out: ${d.reason}.`));
    else if (d.current === "dev") {
      const rb = el("button.mini", { type: "button" }, "Rebuild from source");
      rb.addEventListener("click", () => openFix(rb, "rebuild"));
      rows.push(row("dev", "x", "A development build of Colima Dashboard.", rb));
    } else if (!d.canUpdate && d.reason) rows.push(row("manual", "x", `Colima Dashboard ${d.current}: ${d.reason}.`));
    else if (d.latest) rows.push(row("latest", "ok", `Colima Dashboard ${d.current} is up to date.`));
    const c = u.colima || {};
    if (c.newer) rows.push(row("new", "warn", `Colima ${c.latest.version} is out (you have ${c.current}).${c.canUpdate ? "" : " " + c.reason}`,
      link(c.latest.url, "Release notes"), c.canUpdate ? el("button.mini", { type: "button", onclick: updateColima }, "Update Colima") : null));
    else if (c.latest) rows.push(row("latest", "ok", `Colima ${c.current} is up to date.`));
    if (u.checkError) rows.push(el("div.upd", el("span.small.muted", `Could not check everything: ${u.checkError}`)));
  }
  const checked = u?.checkedAt ? el("div.upd-head-r", el("span.small.muted", `Checked ${ago(Date.parse(u.checkedAt) / 1000)}`),
    el("button.ghost.mini", { type: "button", onclick: checkNow }, "Check now")) : null;
  const news = (u?.colimaNews || []).map((r, i) => el("details", { open: i === 0 && u.colima?.newer },
    el("summary", r.name || r.tag, el("span.muted", new Date(r.published).toLocaleDateString(undefined, { day: "numeric", month: "short", year: "numeric" }))),
    r.notes ? el("p", r.notes) : null,
    el("a.small", { href: r.url, target: "_blank", rel: "noopener noreferrer" }, "Full release notes")));
  return el("section.card", { id: "welcome" },
    el("div.welcome",
      el("h2", el("img", { src: "/static/icon.svg", alt: "", width: 28, height: 28 }), "Welcome to Colima Dashboard"),
      el("label.check", auto, "Update automatically"),
      el("div", el("p.muted", `A local dashboard for Colima: your VMs, containers, images and Kubernetes in one place. Version ${ov.version || "–"}.`),
        el("div.links",
          el("a", { href: "https://github.com/abiosoft/colima", target: "_blank", rel: "noopener noreferrer" }, "Colima on GitHub"),
          el("a", { href: "https://github.com/andrewbakercloudscale/colima-dashboard", target: "_blank", rel: "noopener noreferrer" }, "Colima Dashboard on GitHub"),
          el("a", { href: "https://github.com/andrewbakercloudscale/colima-dashboard/issues", target: "_blank", rel: "noopener noreferrer" }, "Report a problem"))),
      el("div")),
    el("div.upd-list", el("div.upd-head", el("h3", "Updates"), checked), rows),
    news.length ? [el("h3", "What's new in Colima"), el("div.news", news)] : null);
}

// Check now: the answer is often "same as before", so say so instead of silently redrawing.
async function checkNow(e) {
  const btn = e.currentTarget;
  btn.disabled = true;
  btn.textContent = "Checking…";
  await refreshUpdates(true);
  const u = state.updates || {};
  if (u.checkError && !u.colima?.latest) toast(`Could not check: ${u.checkError}`, true);
  else if (u.dashboard?.newer || u.colima?.newer) toast("An update is available");
  else toast(`Everything is up to date${u.colima?.current ? ` (Colima ${u.colima.current})` : ""}`);
}

async function installDashboard() {
  try { state.updates = await api("POST", "/api/updates/install"); refreshUpdates(); }
  catch (e) { toast(e.message, true); }
  // Poll while it downloads; the page reloads by itself once the new version answers.
  const poll = setInterval(async () => {
    try { state.updates = await api("GET", "/api/updates"); } catch { return; }
    const w = $("#welcome"); if (w) w.replaceWith(welcomeCard());
    paintStatusBar();
    if (state.updates.dashboard?.phase !== "downloading") clearInterval(poll);
  }, 1500);
}

async function updateColima() {
  if (!await confirmBox("Update Colima?", "Homebrew upgrades colima. Running profiles keep the old version until you restart them.", "Update", false)) return;
  runJob("POST", "/api/updates/colima", undefined, () => refreshUpdates(true));
}

async function boot() {
  const shownAt = Date.now();
  decorateNav();
  await refreshOverview();
  const ov = state.overview;
  if (ov) $("#splashsub").textContent = ov.colimaVersion ? `Colima ${ov.colimaVersion} · ${ov.profiles.length} profile${ov.profiles.length === 1 ? "" : "s"}` : "Colima was not found";
  await render();
  setTimeout(() => $("#splash").classList.add("gone"), Math.max(0, 700 - (Date.now() - shownAt)));
  refreshStatusInfo();
  setInterval(() => { if (!document.hidden) refreshStatusInfo(); }, 30000);
  refreshUpdates();
  setInterval(() => { if (!document.hidden) refreshUpdates(); }, 30 * 60 * 1000);
  window.addEventListener("online", paintStatusBar);
  window.addEventListener("offline", paintStatusBar);
  // Status in the sidebar stays current; the open tab refreshes itself where it matters.
  setInterval(() => { if (!document.hidden) refreshOverview(); }, 10000);
  // The Diagnostics count in the sidebar, like Rancher Desktop's.
  refreshDiagBadge();
  setInterval(() => { if (!document.hidden) refreshDiagBadge(); }, 30 * 60 * 1000); // its checks run docker on the Mac
  // Re-attach to a job still running from before a reload.
  const running = (state.overview?.jobs || []).filter((j) => !j.done).sort((a, b) => a.started < b.started ? 1 : -1)[0];
  if (running) followJob(running, () => render());
}


// ---------- Monitor: live graphs for the VM and each container ----------

const metrics = { profile: null, samples: [], names: {}, images: {}, slots: new Map(), error: "", hovering: false };

async function pollMetrics() {
  if (metrics.profile !== state.profile) {
    Object.assign(metrics, { profile: state.profile, samples: [], names: {}, images: {}, slots: new Map(), error: "" });
  }
  const since = metrics.samples.length ? metrics.samples[metrics.samples.length - 1].t : 0;
  const d = await api("GET", `${P()}/metrics?since=${since}`);
  metrics.samples.push(...d.samples);
  if (metrics.samples.length > 1800) metrics.samples.splice(0, metrics.samples.length - 1800);
  Object.assign(metrics.names, d.names);
  Object.assign(metrics.images, d.images);
  metrics.error = d.error || "";
  for (const s of d.samples) {
    for (const id of Object.keys(s.containers || {})) {
      const key = cgroup(id); // compose run containers share their service's slot and color
      if (!metrics.slots.has(key)) metrics.slots.set(key, metrics.slots.size); // color only; which groups get their own line is decided per chart
    }
  }
}

const RANGES = { "5m": 5 * 60e3, "15m": 15 * 60e3, "1h": 60 * 60e3 };
// Long ranges come from the history kept on disk (a sample every 5 minutes), not the live samples.
const LONG = { "1d": 1, "7d": 7, "30d": 30 };
const RANGE_LABELS = { "5m": "5 min", "15m": "15 min", "1h": "1 hour", "1d": "1 day", "7d": "7 days", "30d": "30 days" };

function cname(id) { return metrics.names[id] || id.slice(0, 12); }
// "shop-unit-tests-run-f78b48dee08e" (docker compose run) charts as "shop-unit-tests": one line per
// service, not one per test run.
function cgroup(id) { return cname(id).replace(/-run-[0-9a-f]{6,}$/, ""); }
function ccolor(id) { return Charts.seriesColor(metrics.slots.get(cgroup(id)) ?? -1); }
function rate(n) { return `${bytes(n)}/s`; }
function pct(v) { return `${(v || 0).toFixed(v < 10 ? 1 : 0)}%`; }

function avgOver(ms, get) {
  const s = metrics.samples;
  if (!s.length) return null;
  const end = s[s.length - 1].t;
  if (end - s[0].t < ms * 0.8) return null; // not enough history yet
  let sum = 0, n = 0;
  for (let i = s.length - 1; i >= 0 && end - s[i].t <= ms; i--) {
    const v = get(s[i]);
    if (v !== null && v !== undefined) { sum += v; n++; }
  }
  return n ? sum / n : null;
}

function cpuWord(v) { return v < 5 ? "Idle" : v < 25 ? "Lightly used" : v < 60 ? "Moderately busy" : "Busy"; }
function memWord(f) { return f < 0.5 ? "Plenty free" : f < 0.8 ? "Comfortable" : f < 0.92 ? "Getting full" : "Nearly full"; }

function heroRow(gaugePct, label, title, subtitle, chips, avgs) {
  return el("div.hero-row",
    el("div.hero-gauge", Charts.ringGauge(gaugePct), el("span.hero-pct", pct(gaugePct)), el("span.hero-label", label)),
    el("div.hero-text", el("h2", title), el("p", subtitle), el("div.chips", chips.map((c) => el("span.chip", c)))),
    el("div.hero-avgs", avgs.map(([v, l]) => el("div.avg", el("strong", v === null ? "–" : pct(v)), el("span", l)))));
}

async function renderMonitor(main) {
  const range = safeGet("range") || "15m";
  const head = el("div.head",
    el("div", el("h1", "Monitor"), el("p.muted", `Live graphs for profile ${state.profile}: the VM, what it costs your Mac, and every container.`)),
    el("div.seg", Object.keys(RANGE_LABELS).map((r) => el(`button.${r === range ? "on" : "off"}`, { type: "button", "data-range": r, onclick: () => { safeSet("range", r); render(); } }, RANGE_LABELS[r]))));
  if (LONG[range]) {
    const body = el("div", { id: "monitor" }, el("div.card", el("p.muted", "Loading…")));
    main.replaceChildren(head, body);
    await drawHistory(body, LONG[range]);
    setTimer(() => drawHistory(body, LONG[range]), 60000);
    return;
  }
  if (!isRunning()) { main.replaceChildren(head, notRunningCard()); return; }
  const body = el("div", { id: "monitor" });
  body.addEventListener("pointerover", (e) => { metrics.hovering = !!e.target.closest(".tchart"); });
  body.addEventListener("pointerleave", () => { metrics.hovering = false; });
  main.replaceChildren(head, body);
  const tick = async () => {
    try { await pollMetrics(); } catch (e) { metrics.error = e.message; }
    if (!metrics.hovering && document.body.contains(body)) drawMonitor(true);
  };
  await tick();
  setTimer(tick, 2000);
}

function drawMonitor(dataOnly) {
  if (!dataOnly) return render();
  const body = $("#monitor");
  if (!body) return;
  const range = RANGES[safeGet("range")] ? safeGet("range") : "15m";
  for (const b of document.querySelectorAll(".seg button")) b.className = b.dataset.range === range ? "on" : "off";
  const all = metrics.samples;
  const last = all[all.length - 1];
  if (!last || !last.guest) {
    body.replaceChildren(el("div.card.empty", metrics.error ? el("p", metrics.error) : el("p", "Collecting the first samples… graphs appear in a few seconds.")));
    return;
  }
  const cut = last.t - RANGES[range];
  const win = all.filter((s) => s.t >= cut);
  const times = win.map((s) => s.t);
  const g = last.guest, h = last.host || {};
  const memFrac = g.memTotal ? g.memUsed / g.memTotal : 0;

  // hero
  const hero = el("div.hero",
    heroRow(g.cpu, "CPU", cpuWord(g.cpu), `${((g.cpu / 100) * g.cpus).toFixed(1)} of ${g.cpus} vCPUs busy`,
      [`Load ${g.load1.toFixed(2)}`, `On your Mac: ${((h.cpu || 0) / 100).toFixed(2)} cores`],
      [[avgOver(60e3, (s) => s.guest?.cpu), "1 min"], [avgOver(15 * 60e3, (s) => s.guest?.cpu), "15 min"], [avgOver(60 * 60e3, (s) => s.guest?.cpu), "1 hour"]]),
    heroRow(memFrac * 100, "Memory", memWord(memFrac), `${bytes(g.memUsed)} of ${bytes(g.memTotal)} in use inside the VM`,
      [`On your Mac: ${bytes(h.memory)}`, `Docker disk: ${bytes(g.diskUsed)} of ${bytes(g.diskTotal)}`],
      [[avgOver(60e3, (s) => s.guest && 100 * s.guest.memUsed / s.guest.memTotal), "1 min"], [avgOver(15 * 60e3, (s) => s.guest && 100 * s.guest.memUsed / s.guest.memTotal), "15 min"], [avgOver(60 * 60e3, (s) => s.guest && 100 * s.guest.memUsed / s.guest.memTotal), "1 hour"]]),
    el("div.hero-foot", el("span.hero-foot-label", "I/O"),
      el("span.chip", `Network ↓ ${rate(g.netRx)}  ↑ ${rate(g.netTx)}`),
      el("span.chip", `Disk read ${rate(g.diskRead)}  write ${rate(g.diskWrite)}`),
      el("span.chip", `${Object.keys(last.containers).length} running containers`)));

  // containers grouped by service (compose run containers join their service), ordered by slot
  // so colors and order follow the entity
  const ids = [...new Set(win.flatMap((s) => Object.keys(s.containers || {})))];
  const members = new Map();
  for (const id of ids) {
    const k = cgroup(id);
    if (!members.has(k)) members.set(k, []);
    members.get(k).push(id);
  }
  const slotOf = (k) => metrics.slots.get(k) ?? -1;
  const OTHER = "var(--series-other)";
  const sumOf = (list, cs, get) => {
    let v = null;
    for (const id of list) if (cs?.[id]) v = (v ?? 0) + get(cs[id]);
    return v;
  };
  // Each chart names the 6 groups that use the most of what it shows over the range; the rest
  // share one "N more" series. Colors stay with the group from chart to chart.
  const TOP = 6;
  const split = (get) => {
    const total = (k) => win.reduce((a, s) => a + (sumOf(members.get(k), s.containers, get) ?? 0), 0);
    const ranked = [...members.keys()].map((k) => [k, total(k)]).sort((a, b) => b[1] - a[1]).map(([k]) => k);
    const named = ranked.slice(0, TOP).sort((a, b) => slotOf(a) - slotOf(b));
    const rest = ranked.slice(TOP);
    return { named, rest, restIds: rest.flatMap((k) => members.get(k)) };
  };
  const byContainer = (get, rest) => {
    const sp = split(get);
    const series = sp.named.map((k) => ({ name: k, color: Charts.seriesColor(slotOf(k)), values: win.map((s) => sumOf(members.get(k), s.containers, get)) }));
    if (sp.rest.length) series.push({ name: `${sp.rest.length} more`, color: OTHER, values: win.map((s) => sumOf(sp.restIds, s.containers, get) ?? 0) });
    if (rest) series.push({ name: "VM and system", color: "var(--series-system)", values: win.map((s) => { const r = rest(s); return r === null ? null : Math.max(0, r - (sumOf(ids, s.containers, get) ?? 0)); }) });
    return series;
  };

  // right now: CPU share donut, same groups as the CPU chart
  const cpuSplit = split((c) => c.cpu || 0);
  const nowSegs = cpuSplit.named.map((k) => ({ label: k, value: sumOf(members.get(k), last.containers, (c) => c.cpu || 0) ?? 0, color: Charts.seriesColor(slotOf(k)) }));
  if (cpuSplit.rest.length) nowSegs.push({ label: `${cpuSplit.rest.length} more`, value: sumOf(cpuSplit.restIds, last.containers, (c) => c.cpu || 0) ?? 0, color: OTHER });
  const sysNow = Math.max(0, g.cpu - nowSegs.reduce((a, s) => a + s.value, 0));
  nowSegs.push({ label: "VM and system", value: sysNow, color: "var(--series-system)" });
  const rightNow = el("div.card.viz",
    el("div.viz-head", el("h2", "Right now"), el("span.muted.small", "% of the VM's CPU")),
    el("div.donut-wrap", Charts.donut(nowSegs, 168, [pct(g.cpu), "busy"]),
      el("div.donut-legend", nowSegs.map((s) => { const sw = el("span.sw"); sw.style.background = s.color; return el("div.dl-row", sw, el("span.dl-name", s.label), el("span.dl-val", pct(s.value))); }))));

  const chart = (title, note, opts) => el("div.card.viz", el("div.viz-head", el("h2", title), note ? el("span.muted.small", note) : null), Charts.timeChart({ times, domain: [last.t - RANGES[range], last.t], ...opts }));
  const cpuArea = chart(range === "1h" ? "Last hour, by container" : `Last ${range === "15m" ? "15 minutes" : "5 minutes"}, by container`, "% of the VM's CPU",
    { series: byContainer((c) => c.cpu, (s) => s.guest?.cpu ?? null), stacked: true, yMax: 100, fmt: (v) => `${v.toFixed(v < 10 ? 1 : 0)}%` });
  const memArea = chart("Memory, by container", "inside the VM",
    { series: byContainer((c) => c.memory, (s) => s.guest?.memUsed ?? null), stacked: true, fmt: bytes });
  const net = chart("Network", "VM to the outside world", {
    series: [{ name: "Received", color: Charts.seriesColor(0), values: win.map((s) => s.guest?.netRx ?? null) },
      { name: "Sent", color: Charts.seriesColor(1), values: win.map((s) => s.guest?.netTx ?? null) }], fmt: rate });
  const disk = chart("Disk I/O", "VM disks", {
    series: [{ name: "Read", color: Charts.seriesColor(0), values: win.map((s) => s.guest?.diskRead ?? null) },
      { name: "Write", color: Charts.seriesColor(1), values: win.map((s) => s.guest?.diskWrite ?? null) }], fmt: rate });
  const vmMem = chart("VM memory in use", `of ${bytes(g.memTotal)}`, {
    series: [{ name: "In use", color: Charts.seriesColor(0), fill: true, values: win.map((s) => s.guest?.memUsed ?? null) }], fmt: bytes, yMax: g.memTotal });
  const macCpu = chart("Mac CPU used by the VM", "% of one Mac core", {
    series: [{ name: "VM on the Mac", color: Charts.seriesColor(6), fill: true, values: win.map((s) => s.host?.cpu ?? null) }], fmt: (v) => `${v.toFixed(v < 10 ? 1 : 0)}%` });
  const macMem = chart("Mac memory used by the VM", "physical footprint, as in Activity Monitor", {
    series: [{ name: "VM on the Mac", color: Charts.seriesColor(6), fill: true, values: win.map((s) => s.host?.memory ?? null) }], fmt: bytes });

  // per-container table with sparklines
  const rows = ids.filter((id) => last.containers[id]).map((id) => {
    const c = last.containers[id];
    const cpuVals = win.map((s) => s.containers?.[id]?.cpu ?? null);
    const memVals = win.map((s) => s.containers?.[id]?.memory ?? null);
    const sw = el("span.sw"); sw.style.background = ccolor(id);
    return el("tr",
      el("td", el("div.cname", sw, el("strong", cname(id))), el("div.muted.small.mono", metrics.images[id] || "")),
      el("td", el("div.sparkcell", Charts.sparkline(cpuVals, ccolor(id)), el("span.mono", pct(c.cpu)))),
      el("td", el("div.sparkcell", Charts.sparkline(memVals, ccolor(id)), el("span.mono", bytes(c.memory)))),
      el("td.mono.small", `↓ ${rate(c.netRx)}`, el("br"), `↑ ${rate(c.netTx)}`),
      el("td.mono.small", `r ${rate(c.diskRead)}`, el("br"), `w ${rate(c.diskWrite)}`));
  });
  const table = el("div.card.viz", el("div.viz-head", el("h2", "Containers"), el("span.muted.small", "CPU as % of the VM; the line shows the selected range")),
    rows.length ? el("div.table-wrap", el("table",
      el("thead", el("tr", ["Container", "CPU", "Memory", "Network", "Disk"].map((x) => el("th", x)))), el("tbody", rows)))
      : el("p.empty", "No running containers."));

  const note = el("p.small.muted", "Sampled every 2 seconds while this page is open; sampling stops 5 minutes after you leave. Gaps in the lines are times nobody was watching.");
  body.replaceChildren(hero, el("div.viz-grid", rightNow, cpuArea), el("div.viz-grid", memArea, vmMem), el("div.viz-grid", net, disk), el("div.viz-grid", macCpu, macMem), table, note);
}

tabs.monitor = renderMonitor;

// ---------- History: days of samples, runs and builds ----------

function dur(sec) {
  if (sec === undefined || sec === null) return "–";
  if (sec < 1) return `${Math.round(sec * 1000)} ms`;
  if (sec < 90) return `${sec.toFixed(sec < 10 ? 1 : 0)} s`;
  if (sec < 5400) return `${Math.floor(sec / 60)} min ${Math.round(sec % 60)} s`;
  return `${(sec / 3600).toFixed(1)} h`;
}
function pctRate(f) { return f < 0 ? "–" : `${Math.round(f * 100)}%`; }
function whenAgo(t) { return ago(Date.parse(t) / 1000); }
function outcomePill(o) { return el(`span.pill.${{ pass: "ok", fail: "fail", oom: "fail", stopped: "x" }[o] || "x"}`, o === "oom" ? "out of memory" : o); }

function daysSeg(days, onPick) {
  return el("div.seg", [1, 7, 30].map((d) => el(`button.${d === days ? "on" : "off"}`, { type: "button", onclick: () => onPick(d) }, d === 1 ? "1 day" : `${d} days`)));
}

function recordingNote(rec, what) {
  return rec ? el("p.small.muted", `Recorded from Docker's own events while Colima Dashboard runs; kept 30 days on this Mac. ${what}`)
    : el("div.notice", `Profile ${state.profile} is not running, so nothing new is being recorded. Start it and runs are recorded again.`);
}

async function renderRuns(main) {
  const days = Number(safeGet("rundays")) || 7;
  const head = el("div.head",
    el("div", el("h1", "Runs and tests"), el("p.muted", `Every container that ran in ${state.profile}: how long it took and how it ended.`)),
    daysSeg(days, (d) => { safeSet("rundays", String(d)); render(); }));
  main.replaceChildren(head, el("div.card", el("p.muted", "Loading…")));
  const d = await api("GET", `${P()}/runs?days=${days}`);
  if (!d.total) {
    main.replaceChildren(head, el("div.card.empty",
      el("p", "No runs recorded yet."),
      el("p.muted", "Run a test container, for example ", el("code", "docker compose run --rm tests"), " or ", el("code", "docker run --rm your-image npm test"), ", and it appears here with its time and result. Long-running services appear when they stop.")),
    recordingNote(d.recording, ""));
    return;
  }
  const own = d.jobs.reduce((a, j) => a + j.pass + j.fail + j.oom, 0);
  const passed = d.jobs.reduce((a, j) => a + j.pass, 0);
  const flaky = d.jobs.filter((j) => j.flaky);
  const slow = d.jobs.filter((j) => j.slower >= 1.2);
  const failing = d.jobs.filter((j) => j.last.outcome === "fail" || j.last.outcome === "oom");
  const stats = el("div.stat-row",
    stat(String(d.total), "runs"),
    stat(own ? pctRate(passed / own) : "–", "passed", "of runs that ended on their own"),
    stat(String(failing.length), "failing now", "last run failed", failing.length ? "bad" : ""),
    stat(String(flaky.length), "flaky", "passed and failed in the last 10 runs", flaky.length ? "warn" : ""),
    stat(String(slow.length), "slower", "last 5 runs 20% or more slower than before", slow.length ? "warn" : ""));
  const legend = el("div.legend.small", ["pass", "fail", "oom", "stopped"].map((o) => { const sw = el("span.sw"); sw.style.background = Charts.OUTCOME_COLORS[o]; return el("span.lgd", sw, o === "oom" ? "out of memory" : o); }));
  const jobs = el("div.card.viz", el("div.viz-head", el("h2", "By service or image"), legend),
    el("div.table-wrap", el("table",
      el("thead", el("tr", ["", "Runs", "Passed", "Median", "Slowest 10%", "Last run", "Trend"].map((x) => el("th", x)))),
      el("tbody", d.jobs.map((j) => el("tr",
        el("td", el("strong.mono", j.job),
          el("div.flags", j.flaky ? el("span.pill.warn", "flaky") : null,
            j.slower >= 1.2 ? el("span.pill.warn", `${Math.round((j.slower - 1) * 100)}% slower`) : null,
            j.oom ? el("span.pill.fail", `${j.oom} out of memory`) : null,
            j.restarts ? el("span.pill.x", `${j.restarts} restarts`) : null,
            j.unhealthy ? el("span.pill.warn", `${j.unhealthy} unhealthy`) : null)),
        el("td.mono", String(j.runs)),
        el("td.mono", j.passRate < 0 ? el("span.muted", "service") : `${pctRate(j.passRate)}`, j.fail ? el("div.muted.small", `${j.fail} failed`) : null),
        el("td.mono.nowrap", j.passRate < 0 ? "–" : dur(j.median)),
        el("td.mono.nowrap", j.passRate < 0 ? "–" : dur(j.p90)),
        el("td", outcomePill(j.last.outcome), el("div.muted.small", `${whenAgo(j.last.t)}, ${dur(j.last.seconds)}`)),
        el("td", Charts.runBars(j.trend, dur))))))));
  const recent = el("div.card.viz", el("div.viz-head", el("h2", "Recent runs"), el("span.muted.small", `newest first, ${d.total} in all`)),
    el("div.table-wrap", el("table",
      el("thead", el("tr", ["When", "Container", "Result", "Exit code", "Took"].map((x) => el("th", x)))),
      shortBody(d.recent.slice(0, 100).map((r) => el("tr",
        el("td.small.nowrap", new Date(r.t).toLocaleString([], { day: "numeric", month: "short", hour: "2-digit", minute: "2-digit", second: "2-digit" })),
        el("td", el("strong", r.name), el("div.muted.small.mono", r.job)),
        el("td", outcomePill(r.outcome), r.restarted ? el("span.muted.small", " restarted") : null),
        el("td.mono", String(r.exit)),
        el("td.mono.nowrap", dur(r.seconds))))))));
  main.replaceChildren(head, stats, jobs, recent,
    recordingNote(d.recording, "A run counts as passed when it exits 0 by itself; stopping a container yourself is not a failure."));
}

// shortBody shows the first 12 rows of a long table, with a row to show the rest.
function shortBody(rows, n = 12) {
  const body = el("tbody", rows.slice(0, n));
  if (rows.length > n) {
    const more = el("button.ghost.mini", { type: "button" }, `Show ${rows.length - n} more`);
    const tr = el("tr", el("td", { colspan: 9 }, more));
    more.addEventListener("click", () => body.replaceChildren(...rows));
    body.append(tr);
  }
  return body;
}

function stat(value, label, title, tone) {
  return el(`div.stat${tone ? "." + tone : ""}`, { title: title || "" }, el("strong", value), el("span", label));
}

async function renderBuilds(main) {
  const days = Number(safeGet("builddays")) || 7;
  const head = el("div.head",
    el("div", el("h1", "Builds"), el("p.muted", `Image builds in ${state.profile}: how long each took, how much came from cache, and the trend.`)),
    daysSeg(days, (d) => { safeSet("builddays", String(d)); render(); }));
  main.replaceChildren(head, el("div.card", el("p.muted", "Loading…")));
  const d = await api("GET", `${P()}/builds?days=${days}`);
  const parts = [head];
  if (d.error) {
    const noBuildx = /buildx is not installed/.test(d.error);
    parts.push(el("div.notice.bad", el("p", "Builds cannot be read: ", d.error, "."),
      noBuildx ? fixRow({ id: "buildx", title: "docker buildx", fix: "brew install docker-buildx", action: "terminal" }) : null));
  }
  if (!d.total) {
    parts.push(el("div.card.empty", el("p", "No builds recorded yet."),
      el("p.muted", "Build an image, for example ", el("code", "docker build -t app ."), " or ", el("code", "docker compose build"), ", and it appears here within two minutes.")));
    parts.push(recordingNote(d.recording, ""));
    main.replaceChildren(...parts);
    return;
  }
  const slow = d.names.filter((b) => b.slower >= 1.2);
  const completed = d.names.reduce((a, b) => a + b.builds - b.failed, 0);
  const failed = d.names.reduce((a, b) => a + b.failed, 0);
  parts.push(el("div.stat-row",
    stat(String(d.total), "builds"),
    stat(String(failed), "failed", "", failed ? "bad" : ""),
    stat(String(slow.length), "slower", "last 5 builds 20% or more slower than before", slow.length ? "warn" : ""),
    stat(String(d.names.length), "images", "distinct build names")));
  parts.push(el("div.card.viz", el("div.viz-head", el("h2", "By image"), el("span.muted.small", `${completed} completed`)),
    el("div.table-wrap", el("table",
      el("thead", el("tr", ["", "Builds", "Median", "From cache", "Last build", "Trend"].map((x) => el("th", x)))),
      el("tbody", d.names.map((b) => el("tr",
        el("td", el("strong.mono", b.name || "(unnamed)"), el("div.flags",
          b.slower >= 1.2 ? el("span.pill.warn", `${Math.round((b.slower - 1) * 100)}% slower`) : null,
          b.failed ? el("span.pill.fail", `${b.failed} failed`) : null)),
        el("td.mono", String(b.builds)),
        el("td.mono.nowrap", b.builds > b.failed ? dur(b.median) : "–"), // no completed build, no median
        el("td.mono", pctRate(b.cacheRate), el("div.muted.small", "of steps")),
        el("td", outcomePill(b.last.status === "Completed" ? "pass" : "fail"), el("div.muted.small", `${whenAgo(b.last.t)}, ${dur(b.last.seconds)}`)),
        el("td", Charts.runBars(b.trend, dur)))))))));
  parts.push(el("div.card.viz", el("div.viz-head", el("h2", "Recent builds"), el("span.muted.small", "newest first")),
    el("div.table-wrap", el("table",
      el("thead", el("tr", ["When", "Image", "Result", "Steps", "Took"].map((x) => el("th", x)))),
      shortBody(d.recent.slice(0, 100).map((b) => el("tr",
        el("td.small.nowrap", new Date(b.t).toLocaleString([], { day: "numeric", month: "short", hour: "2-digit", minute: "2-digit" })),
        el("td", el("strong.mono", b.name || "(unnamed)"), b.error ? el("div.small.bad-text", b.error) : null),
        el("td", outcomePill(b.status === "Completed" ? "pass" : "fail")),
        el("td.mono.nowrap", `${b.cached}/${b.steps} cached`),
        el("td.mono.nowrap", dur(b.seconds)))))))));
  parts.push(recordingNote(d.recording, "Read from BuildKit's build history every 2 minutes."));
  main.replaceChildren(...parts);
}

async function drawHistory(body, days) {
  const d = await api("GET", `${P()}/history?days=${days}`);
  const pts = d.points || [];
  if (pts.length < 2) {
    body.replaceChildren(el("div.card.empty", el("p", "Not enough history yet."),
      el("p.muted", "Colima Dashboard samples each running profile every 5 minutes and keeps 30 days. The first lines appear after about 10 minutes.")),
      recordingNote(d.recording, ""));
    return;
  }
  const times = pts.map((p) => Date.parse(p.t));
  const end = Date.now();
  const domain = [end - days * 86400e3, end];
  const opts = { times, domain, gapMs: 16 * 60e3 };
  const chart = (title, note, o) => el("div.card.viz", el("div.viz-head", el("h2", title), note ? el("span.muted.small", note) : null), Charts.timeChart({ ...opts, ...o }));
  const pctFmt = (v) => `${v.toFixed(v < 10 ? 1 : 0)}%`;
  const cards = [];
  const f = d.forecast;
  if (f && !f.reliable) {
    cards.push(el("div.notice.ok", el("strong", "VM disk: "), `${bytes(f.used)} of ${bytes(f.size)} used on your Mac. `,
      "A forecast of when it fills appears after a day of history."));
  } else if (f) {
    const full = f.days >= 0 && f.days < 3650;
    cards.push(el(`div.notice${full && f.days < 30 ? ".bad" : full && f.days < 90 ? "" : ".ok"}`,
      el("strong", "VM disk: "), `${bytes(f.used)} of ${bytes(f.size)} used on your Mac. `,
      f.perDay > 1 << 20 ? `Growing about ${bytes(f.perDay)} a day over the last ${f.window.toFixed(f.window < 2 ? 1 : 0)} days` : "Not growing",
      full ? `, so it fills in about ${Math.round(f.days)} days.` : ".",
      full && f.days < 90 ? el("span", " ", el("a", { href: "#troubleshooting" }, "Clean up downloads"), " or prune unused images.") : null));
  }
  cards.push(el("div.viz-grid",
    chart("Mac CPU used by the VM", "% of one Mac core, 5 minute average", { series: [{ name: "VM on the Mac", color: Charts.seriesColor(6), fill: true, values: pts.map((p) => p.hostCpu ?? null) }], fmt: pctFmt }),
    chart("Mac memory used by the VM", "physical footprint", { series: [{ name: "VM on the Mac", color: Charts.seriesColor(6), fill: true, values: pts.map((p) => p.hostMem || null) }], fmt: bytes })));
  const dockerPts = pts.filter((p) => p.images !== undefined);
  const dtimes = dockerPts.map((p) => Date.parse(p.t));
  cards.push(el("div.viz-grid",
    chart("VM disk on your Mac", "space the VM's disk image takes", { series: [{ name: "On the Mac", color: Charts.seriesColor(2), fill: true, values: pts.map((p) => p.diskMac || null) }], fmt: bytes }),
    el("div.card.viz", el("div.viz-head", el("h2", "Docker disk use"), el("span.muted.small", "every 15 minutes")),
      Charts.timeChart({ times: dtimes, domain, gapMs: 31 * 60e3, stacked: true, fmt: bytes, empty: "Not sampled yet.",
        series: [["Images", "images", 0], ["Build cache", "buildCache", 1], ["Volumes", "volumes", 3], ["Containers", "containers", 4]]
          .map(([name, k, c]) => ({ name, color: Charts.seriesColor(c), values: dockerPts.map((p) => p[k] ?? null) })) }))));
  const guest = pts.some((p) => p.guestCpu !== undefined);
  cards.push(el("div.viz-grid",
    chart("Running containers", "", { series: [{ name: "Running", color: Charts.seriesColor(0), values: pts.map((p) => p.running ?? null) }], fmt: (v) => v.toFixed(0) }),
    guest ? chart("CPU inside the VM", "only while the Monitor was open", { series: [{ name: "VM CPU", color: Charts.seriesColor(1), values: pts.map((p) => p.guestCpu ?? null) }], fmt: pctFmt, yMax: 100 }) : el("div")));
  body.replaceChildren(...cards, recordingNote(d.recording, "A sample every 5 minutes for each running profile."));
}

tabs.runs = renderRuns;
tabs.builds = renderBuilds;

boot();
