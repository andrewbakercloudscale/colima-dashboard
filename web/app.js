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
  const label = c.action === "start" ? `Start ${state.profile}` : c.action === "restart" ? `Restart ${state.profile}` : "Run in Terminal";
  const run = el(c.confirm ? "button.danger.mini" : "button.mini", { type: "button" }, label);
  run.addEventListener("click", async () => {
    if (c.confirm && !await confirmBox(`${c.title}: fix it?`,
      el("div", el("p", c.confirm), el("p.muted.small", "Runs in Terminal: "), el("pre.log", c.fix)), "Run it", true)) return;
    if (c.action === "start") return startProfile();
    if (c.action === "restart") return runJob("POST", `${P()}/actions/restart`, undefined, () => render());
    await openFix(run, c.id);
  });
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

function needsFix(c) { return (c.state === "fail" || c.state === "warn") && c.id !== "vm"; }

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
  $("#drawertitle").textContent = title;
  $("#drawerbody").replaceChildren(...content);
  $("#drawer").hidden = false;
}
function closeDrawer() {
  $("#drawer").hidden = true;
  if (state.logSource) { state.logSource.close(); state.logSource = null; }
}
$("#drawerclose").addEventListener("click", closeDrawer);
$("#drawer").addEventListener("click", (e) => { if (e.target.id === "drawer") closeDrawer(); });
document.addEventListener("keydown", (e) => { if (e.key === "Escape" && !$("#drawer").hidden && $("#modal").hidden) closeDrawer(); });

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

window.addEventListener("hashchange", render);

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
  const tab = (location.hash || "#general").slice(1);
  state.tab = tabs[tab] ? tab : "general";
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
  const vm = el("div.card", el("h2", `Profile ${state.profile}`));
  if (p) {
    vm.append(el("dl.kv",
      el("dt", "State"), el("dd", el(`span.pill.${running ? "running" : "x"}`, p.status)),
      el("dt", "Runtime"), el("dd", p.runtime || "–"),
      el("dt", "Architecture"), el("dd", p.arch || "–"),
      el("dt", "CPUs"), el("dd", String(p.cpus || "–")),
      el("dt", "Memory"), el("dd", gib(p.memory)),
      el("dt", "Disk"), el("dd", gib(p.disk)),
      p.address ? [el("dt", "Address"), el("dd", p.address)] : null));
  } else {
    vm.append(el("p.muted", "This profile does not exist yet. Start creates it with Colima's defaults."));
  }
  cards.append(vm);
  const attention = el("div.attention");
  main.replaceChildren(head, attention, ...(boot ? [boot] : [welcomeCard()]), cards);
  api("GET", `/api/diagnostics?profile=${encodeURIComponent(state.profile)}`).then((list) => {
    setDiagBadge(list);
    const todo = list.filter(needsFix);
    if (!todo.length) return;
    attention.replaceChildren(el("div.card", el("h2", todo.length === 1 ? "1 thing to fix" : `${todo.length} things to fix`),
      el("table", el("tbody", todo.map((c) => el("tr",
        el("td", el(`span.pill.${c.state}`, c.state)),
        el("td", el("strong", c.title), el("div.muted.small", c.detail), fixRow(c))))))));
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
    el("div.toolbar", el("input", { type: "search", id: "cfilter", placeholder: "Filter", "aria-label": "Filter containers", value: safeGet("cfilter") || "" })));
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
        tbody.append(el("tr.group", el("td", { colspan: 5 }, `Compose: ${g}`, el("span.muted.small", ` · ${cs.length} ${cs.length === 1 ? "container" : "containers"}`)),
          el("td.actions", el("button.ghost.mini", { type: "button", onclick: () => groupAction(cs, anyRunning ? "stop" : "start") }, anyRunning ? "Stop all" : "Start all"))));
      }
      for (const c of cs) tbody.append(containerRow(c));
    }
    body.replaceChildren(el("table",
      el("thead", el("tr", ["Name", "Image", "State", "Ports", "Created", ""].map((h) => el("th", h)))), tbody));
  };
  const load = async () => {
    try { list = await api("GET", `${P()}/containers`); draw(); } catch (e) { body.replaceChildren(el("div.notice.bad", e.message)); }
  };
  filter.addEventListener("input", draw);
  await load();
  setTimer(load, 4000);
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
    el("td.mono.small", c.Image),
    el("td", el(`span.pill.${c.State}`, c.State), el("div.muted.small", c.Status)),
    el("td", ports.length ? ports : el("span.muted", "–")),
    el("td.small", ago(c.Created)),
    el("td.actions",
      running ? el("button.ghost.mini", { type: "button", onclick: act("stop") }, "Stop") : null,
      running ? el("button.ghost.mini", { type: "button", onclick: act("pause") }, "Pause") : null,
      paused ? el("button.ghost.mini", { type: "button", onclick: act("unpause") }, "Resume") : null,
      !running && !paused ? el("button.ghost.mini", { type: "button", onclick: act("start") }, "Start") : null,
      el("button.ghost.mini", { type: "button", onclick: act("restart") }, "Restart"),
      el("button.ghost.mini", { type: "button", onclick: () => showLogs(c.Id, name) }, "Logs"),
      el("button.ghost.mini", { type: "button", onclick: () => showInspect(c.Id, name) }, "Inspect"),
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

function showLogs(id, name) {
  const log = el("pre.log");
  const search = el("input", { type: "search", placeholder: "Highlight", "aria-label": "Highlight text in logs" });
  const follow = el("input", { type: "checkbox", checked: true });
  const stamps = el("input", { type: "checkbox" });
  const lines = [];
  const paint = () => {
    const q = search.value;
    log.replaceChildren(...lines.map((l) => lineNode(l, q)));
    log.scrollTop = log.scrollHeight;
  };
  const start = () => {
    if (state.logSource) state.logSource.close();
    lines.length = 0;
    log.replaceChildren();
    const qs = `tail=500&follow=${follow.checked ? 1 : 0}&timestamps=${stamps.checked ? 1 : 0}`;
    const src = new EventSource(`${P()}/containers/${encodeURIComponent(id)}/logs?${qs}`);
    state.logSource = src;
    src.addEventListener("line", (e) => {
      const l = JSON.parse(e.data);
      lines.push(l);
      if (lines.length > 5000) lines.shift();
      const atBottom = log.scrollTop + log.clientHeight >= log.scrollHeight - 30;
      log.append(lineNode(l, search.value));
      if (log.childNodes.length > 5000) log.firstChild.remove();
      if (atBottom) log.scrollTop = log.scrollHeight;
    });
    src.addEventListener("done", () => src.close());
    src.addEventListener("failed", (e) => { src.close(); log.append(el("span.err", JSON.parse(e.data) + "\n")); });
  };
  search.addEventListener("input", paint);
  follow.addEventListener("change", start);
  stamps.addEventListener("change", start);
  openDrawer(`Logs: ${name}`,
    el("div.toolbar", search, el("label.check", follow, "Follow"), el("label.check", stamps, "Timestamps"),
      el("button.ghost.mini", { type: "button", onclick: () => download(`${name}.log`, lines.map((l) => l.t).join("\n")) }, "Download")),
    log);
  start();
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
  try {
    const [doc, stats] = await Promise.all([
      api("GET", `${P()}/containers/${encodeURIComponent(id)}`),
      api("GET", `${P()}/containers/${encodeURIComponent(id)}/stats`).catch(() => null),
    ]);
    const cfg = doc.Config || {};
    const host = doc.HostConfig || {};
    const parts = [];
    if (stats && doc.State?.Running) {
      parts.push(el("div.card", el("h2", "Now"), el("dl.kv",
        el("dt", "CPU"), el("dd", `${stats.cpuPercent.toFixed(1)} %`),
        el("dt", "Memory"), el("dd", `${bytes(stats.memoryBytes)} of ${bytes(stats.memoryLimit)}`))));
    }
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

// ---------- Images ----------

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
    el("div", el("h1", "Images"), el("p.muted", "Pull, remove, scan and prune images.")),
    el("div.toolbar", ref, pull, el("button.ghost", { type: "button", onclick: pruneImages }, "Prune unused")));
  if (!isRunning()) { main.replaceChildren(head, notRunningCard()); return; }
  const body = el("div.card.table-wrap", el("p.muted", "Loading…"));
  main.replaceChildren(head, body);
  const imgs = await api("GET", `${P()}/images`);
  if (!imgs.length) { body.replaceChildren(el("p.empty", "No images yet.")); return; }
  const rows = [];
  for (const i of imgs) {
    const tags = (i.RepoTags || []).filter((t) => t !== "<none>:<none>");
    const label = tags[0] || (i.RepoDigests?.[0] || "").split("@")[0] + " (untagged)";
    rows.push(el("tr",
      el("td", el("strong.mono", label), tags.length > 1 ? el("div.muted.small.mono", tags.slice(1).join(", ")) : null),
      el("td.mono.small", short(i.Id)),
      el("td", bytes(i.Size)),
      el("td.small", ago(i.Created)),
      el("td", i.Containers > 0 ? el("span.pill.running", `in use (${i.Containers})`) : el("span.pill", "unused")),
      el("td.actions",
        el("button.ghost.mini", { type: "button", onclick: () => scanImage(tags[0] || i.Id) }, "Scan"),
        el("button.ghost.mini.danger", { type: "button", onclick: () => removeImage(i, label) }, "Remove"))));
  }
  body.replaceChildren(el("table",
    el("thead", el("tr", ["Image", "ID", "Size", "Created", "Used by", ""].map((h) => el("th", h)))), el("tbody", rows)));
}

async function removeImage(i, label) {
  const inUse = i.Containers > 0;
  if (!await confirmBox(`Remove ${label}?`, inUse ? "Containers use this image; remove them first, or the engine refuses." : "The image is deleted from this profile; pull it again to get it back.", "Remove", true)) return;
  try { await api("DELETE", `${P()}/images/${encodeURIComponent(i.Id)}`); toast(`Removed ${label}`); render(); } catch (e) { toast(e.message, true); }
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
      el("td.actions", el("button.ghost.mini.danger", { type: "button", onclick: () => removeVolume(v) }, "Remove")))))));
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
  const head = el("div.head", el("div", el("h1", "Kubernetes"), el("p.muted", "k3s inside the Colima VM.")));
  if (!isRunning()) { main.replaceChildren(head, notRunningCard()); return; }
  main.replaceChildren(head, el("div.card", el("p.muted", "Loading…")));
  const k = await api("GET", `${P()}/kubernetes`);
  const toggle = el("button", { type: "button" }, k.enabled ? "Turn Kubernetes off" : "Turn Kubernetes on");
  toggle.addEventListener("click", async () => {
    const on = !k.enabled;
    if (!await confirmBox(on ? "Turn Kubernetes on?" : "Turn Kubernetes off?",
      `Colima restarts profile ${state.profile} to ${on ? "start" : "stop"} k3s. Running containers stop for a minute.`, on ? "Turn on" : "Turn off", false)) return;
    runJob("POST", `${P()}/actions/apply`, { kubernetes: on }, () => render());
  });
  const card = el("div.card", el("h2", `Kubernetes is ${k.enabled ? "on" : "off"}`), toggle);
  const parts = [card];
  if (k.enabled) {
    if (k.version) card.append(el("pre.log", k.version.trim()));
    parts.push(el("div.card", el("h2", "Nodes"), el("pre.log", (k.nodes || k.nodesError || "").trim())));
    const ctx = el("div.card", el("h2", "kubectl context"));
    if (k.currentContextError) {
      ctx.append(el("p", k.currentContextError + ". Colima wrote the context ", el("code", k.context), " to ~/.kube/config."), fixRow({ id: "kubectx", title: "kubectl", fix: "brew install kubectl", action: "terminal" }));
    } else if (k.currentContext !== k.context) {
      ctx.append(el("p", "kubectl points at ", el("code", k.currentContext), ", not this profile."), fixRow({ id: "kubectx", title: "kubectl context", fix: `kubectl config use-context ${k.context}`, action: "terminal" }));
    } else {
      ctx.append(el("p", el("span.pill.ok", "ok"), " kubectl uses ", el("code", k.context), "."));
    }
    parts.push(ctx);
  }
  main.replaceChildren(head, ...parts);
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
  main.replaceChildren(head, form, el("div.toolbar", apply), el("p"), profiles,
    el("p.small.muted", "To delete this profile, use Factory reset under Troubleshooting."));
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
  checks.replaceChildren(el("table", el("tbody", list.map((c) => el("tr",
    el("td", el(`span.pill.${c.state}`, c.state)),
    el("td", el("strong", c.title), el("div.muted.small", c.detail), fixRow(c)))))));
}

function setDiagBadge(list) {
  const b = $("#diagbadge");
  const fails = list.filter((c) => c.state === "fail").length;
  const warns = list.filter((c) => c.state === "warn").length;
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
  const rows = [];
  if (!u) rows.push(el("p.muted.small", "Checking for updates…"));
  else {
    const d = u.dashboard || {};
    if (d.phase === "downloading") rows.push(el("p.upd", el("span.pill.busy", "updating"), ` Downloading Colima Dashboard ${d.latest?.version}…`));
    else if (d.phase === "installed") rows.push(el("p.upd", el("span.pill.ok", "installed"), ` Version ${d.latest?.version} is installed; the dashboard restarts when no task is running.`));
    else if (d.phase === "failed") rows.push(el("p.upd", el("span.pill.fail", "failed"), ` Update failed: ${d.error}`));
    else if (d.newer && d.canUpdate) rows.push(el("p.upd", el("span.pill.warn", "new"), ` Colima Dashboard ${d.latest.version} is out. `,
      el("button.mini", { type: "button", onclick: installDashboard }, `Update to ${d.latest.version}`), " ", el("a", { href: d.latest.url, target: "_blank", rel: "noopener noreferrer" }, "What changed")));
    else if (d.newer) rows.push(el("p.upd", el("span.pill.warn", "new"), ` Colima Dashboard ${d.latest.version} is out: ${d.reason}.`));
    else if (d.current === "dev") {
      const rb = el("button.mini", { type: "button" }, "Rebuild from source");
      rb.addEventListener("click", () => openFix(rb, "rebuild"));
      rows.push(el("p.upd", el("span.pill.x", "dev"), " A development build of Colima Dashboard. ", rb));
    } else if (!d.canUpdate && d.reason) rows.push(el("p.upd.muted", `Colima Dashboard ${d.current}: ${d.reason}.`));
    else if (d.latest) rows.push(el("p.upd", el("span.pill.ok", "latest"), ` Colima Dashboard ${d.current} is up to date.`));
    const c = u.colima || {};
    if (c.newer) rows.push(el("p.upd", el("span.pill.warn", "new"), ` Colima ${c.latest.version} is out (you have ${c.current}). `,
      c.canUpdate ? el("button.mini", { type: "button", onclick: updateColima }, "Update Colima") : el("span.muted", c.reason), " ",
      el("a", { href: c.latest.url, target: "_blank", rel: "noopener noreferrer" }, "Release notes")));
    else if (c.latest) rows.push(el("p.upd", el("span.pill.ok", "latest"), ` Colima ${c.current} is up to date.`));
    if (u.checkError) rows.push(el("p.small.muted", `Could not check everything: ${u.checkError}`));
    if (u.checkedAt) rows.push(el("p.small.muted", `Checked ${ago(Date.parse(u.checkedAt) / 1000)}. `,
      el("button.ghost.mini", { type: "button", onclick: checkNow }, "Check now")));
  }
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
    el("div", rows),
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
  setInterval(() => { if (!document.hidden) refreshOverview(); }, 5000);
  // The Diagnostics count in the sidebar, like Rancher Desktop's.
  refreshDiagBadge();
  setInterval(() => { if (!document.hidden) refreshDiagBadge(); }, 5 * 60 * 1000);
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
      if (!metrics.slots.has(id)) metrics.slots.set(id, metrics.slots.size < 7 ? metrics.slots.size : -1);
    }
  }
}

const RANGES = { "5m": 5 * 60e3, "15m": 15 * 60e3, "1h": 60 * 60e3 };

function cname(id) { return metrics.names[id] || id.slice(0, 12); }
function ccolor(id) { return Charts.seriesColor(metrics.slots.get(id) ?? -1); }
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
    el("div.seg", Object.keys(RANGES).map((r) => el(`button.${r === range ? "on" : "off"}`, { type: "button", onclick: () => { safeSet("range", r); drawMonitor(); } }, r === "1h" ? "1 hour" : r === "15m" ? "15 min" : "5 min"))));
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
  const range = safeGet("range") || "15m";
  for (const b of document.querySelectorAll(".seg button")) b.className = b.textContent.startsWith(range === "1h" ? "1" : range === "15m" ? "15" : "5") ? "on" : "off";
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

  // containers, ordered by slot so colors and order follow the entity
  const ids = [...new Set(win.flatMap((s) => Object.keys(s.containers || {})))].sort((a, b) => (metrics.slots.get(a) ?? 99) - (metrics.slots.get(b) ?? 99));
  const named = ids.filter((id) => (metrics.slots.get(id) ?? -1) >= 0);
  const others = ids.filter((id) => (metrics.slots.get(id) ?? -1) < 0);
  const OTHER = "var(--series-other)";
  const byContainer = (get, rest) => {
    const series = named.map((id) => ({ name: cname(id), color: ccolor(id), values: win.map((s) => (s.containers?.[id] ? get(s.containers[id]) : null)) }));
    if (others.length) series.push({ name: `${others.length} more containers`, color: OTHER, values: win.map((s) => others.reduce((a, id) => a + (s.containers?.[id] ? get(s.containers[id]) : 0), 0)) });
    if (rest) series.push({ name: "VM and system", color: "var(--series-system)", values: win.map((s) => { const r = rest(s); return r === null ? null : Math.max(0, r - ids.reduce((a, id) => a + (s.containers?.[id] ? get(s.containers[id]) : 0), 0)); }) });
    return series;
  };

  // right now: CPU share donut
  const nowSegs = named.map((id) => ({ label: cname(id), value: last.containers[id]?.cpu || 0, color: ccolor(id) }));
  if (others.length) nowSegs.push({ label: `${others.length} more`, value: others.reduce((a, id) => a + (last.containers[id]?.cpu || 0), 0), color: OTHER });
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

boot();
