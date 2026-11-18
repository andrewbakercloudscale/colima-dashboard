// Containers page extras: health, out of memory and limit cells for the list, the limits
// editor, and the log viewer (one container, or a whole Compose project interleaved by time).
// Loaded before app.js; it only uses app.js helpers (el, api, P, state, toast, openDrawer,
// download, confirmBox, safeGet, safeSet) when called, after both have loaded.
"use strict";

// ---------- list cells ----------

// memText prints bytes in the binary units Docker's flags use: 512 MiB, 1.5 GiB.
function memText(b) {
  if (!b || b <= 0) return "";
  if (b >= 1 << 30) return `${+(b / (1 << 30)).toFixed(2)} GiB`;
  if (b >= 1 << 20) return `${+(b / (1 << 20)).toFixed(0)} MiB`;
  return `${+(b / 1024).toFixed(0)} KiB`;
}
// memFlag prints bytes as a docker run / compose value: 512m, 1.5g.
function memFlag(b) {
  if (!b || b <= 0) return "";
  const GiB = 2 ** 30, MiB = 2 ** 20; // not 1 << 30: shifts are 32-bit in JavaScript
  if (b % GiB === 0 || b >= 4 * GiB) return `${+(b / GiB).toFixed(2)}g`;
  return `${+(b / MiB).toFixed(0)}m`;
}

// State cell extras: health from the list's status text, and out of memory from inspect.
function containerFlags(c) {
  const out = [];
  const d = c.details;
  if (d?.oomKilled && c.State !== "running") {
    out.push(el("span.pill.oom", {
      title: `The kernel killed this container for using more than its memory limit${d.memory ? ` (${memText(d.memory)})` : ""}` +
        `${d.finishedAt && !d.finishedAt.startsWith("0001") ? ` at ${new Date(d.finishedAt).toLocaleString()}` : ""}. Exit code ${d.exitCode}. Raise the limit with Limits.`,
    }, "out of memory"), fixSlot(adviceFor.container(c.Id, ["oom"])));
  }
  if (c.health) {
    const tone = c.health === "healthy" ? "ok" : c.health === "unhealthy" ? "bad" : "warn";
    out.push(el(`span.pill.health.${tone}`, { title: `Healthcheck: ${c.health}` }, c.health === "starting" ? "health: starting" : c.health));
  }
  if (c.State === "running") {
    // close to a limit, held back by one, or no limit and big: a pill and its Fix, from the advice
    out.push(fixSlot(adviceFor.container(c.Id, ["memory-near-limit", "cpu-at-limit", "memory-no-limit"]), {
      render: (is) => el("span.fix-pair", el(`span.pill.${is.severity === "bad" ? "fail" : "warn"}`, { title: is.why }, RUNNING_FLAG[is.kind]), fixButton(is)),
    }));
  }
  return el("div.cflags", out);
}

const RUNNING_FLAG = { "memory-near-limit": "near memory limit", "cpu-at-limit": "CPU limited", "memory-no-limit": "big, no limit" };

// Limits cell: memory and CPU limits, muted "no limit" when unset.
function limitsCell(c) {
  const d = c.details;
  if (!d) return el("td.small.muted", "–");
  const mem = d.memory > 0 ? el("span", memText(d.memory)) : el("span.muted", "no limit");
  const cpu = c.cpus > 0 ? el("span", `${+c.cpus.toFixed(2)} CPU`) : el("span.muted", "no limit");
  return el("td.small.limits", { title: `Memory limit ${d.memory > 0 ? memText(d.memory) : "none"}${d.memoryReservation > 0 ? `, reservation ${memText(d.memoryReservation)}` : ""}; CPU limit ${c.cpus > 0 ? c.cpus : "none"}; restart ${d.restart}` },
    el("div", el("span.lim-k", "mem "), mem), el("div", el("span.lim-k", "cpu "), cpu));
}

// ---------- limits editor ----------

const MEM_PRESETS = ["128m", "256m", "512m", "1g", "2g", "4g"];
const CPU_PRESETS = ["0.25", "0.5", "1", "2"];

// prefill (optional) puts suggested values in the fields, for example a Fix recommendation: { memory: "512m", cpus: "1" }.
async function showLimits(c, name, prefill) {
  let d;
  try { d = await api("GET", `${P()}/containers/${encodeURIComponent(c.Id)}`); } catch (e) { toast(e.message, true); return; }
  const hc = d.HostConfig || {};
  const cpusNow = hc.NanoCpus > 0 ? hc.NanoCpus / 1e9 : hc.CpuQuota > 0 ? hc.CpuQuota / (hc.CpuPeriod || 100000) : 0;
  const cur = {
    memory: memFlag(hc.Memory), reservation: memFlag(hc.MemoryReservation),
    cpus: cpusNow ? String(+cpusNow.toFixed(2)) : "", restart: hc.RestartPolicy?.Name || "no", retries: hc.RestartPolicy?.MaximumRetryCount || 0,
  };
  const labels = d.Config?.Labels || {};
  const service = labels["com.docker.compose.service"] || "";
  const input = (val, ph, label) => el("input.mono", { type: "text", value: val, placeholder: ph, "aria-label": label, spellcheck: false, autocomplete: "off" });
  const mem = input(prefill?.memory || cur.memory, "no limit", "Memory limit");
  const res = input(cur.reservation, "none", "Memory reservation");
  const cpus = input(prefill?.cpus || cur.cpus, "no limit", "CPUs");
  const restart = el("select", { "aria-label": "Restart policy" },
    ["no", "on-failure", "unless-stopped", "always"].map((p) => el("option", { value: p, selected: p === cur.restart }, p)));
  const retries = el("input", { type: "number", min: 0, max: 1000, value: cur.retries, "aria-label": "Maximum retries", class: "lim-retries" });
  const presets = (list, target) => el("div.lim-presets", list.map((v) => el("button.ghost.mini", { type: "button", onclick: () => { target.value = v; refresh(); } }, v)));
  const hint = el("p.small.lim-hint");
  const snippet = el("pre.log.lim-snippet");
  const runLine = el("pre.log.lim-snippet");
  const apply = el("button", { type: "button" }, "Apply");
  const err = el("p.small.bad-text", { hidden: true });

  const refresh = () => {
    retries.disabled = restart.value !== "on-failure";
    if (retries.disabled) retries.value = 0;
    const notes = [];
    if (cur.memory && !mem.value.trim()) notes.push("Docker cannot remove a memory limit from an existing container; leaving it empty keeps the current limit. Recreate the container to remove it.");
    if (cur.cpus && !cpus.value.trim()) notes.push("Docker cannot remove a CPU limit from an existing container; leaving it empty keeps the current limit.");
    if (cur.reservation && !res.value.trim()) notes.push("An empty reservation keeps the current one.");
    hint.textContent = notes.join(" ");
    hint.hidden = !notes.length;
    const m = mem.value.trim() || cur.memory, r = res.value.trim() || cur.reservation, cp = cpus.value.trim() || cur.cpus;
    const rp = restart.value === "on-failure" && +retries.value ? `on-failure:${+retries.value}` : restart.value;
    const y = [`services:`, `  ${service || name}:`];
    if (rp !== "no") y.push(`    restart: ${rp}`);
    if (m || r || cp) {
      y.push("    deploy:", "      resources:");
      if (m || cp) {
        y.push("        limits:");
        if (cp) y.push(`          cpus: "${cp}"`);
        if (m) y.push(`          memory: ${m}`);
      }
      if (r) y.push("        reservations:", `          memory: ${r}`);
    }
    snippet.textContent = y.join("\n");
    runLine.textContent = ["docker run", m && `--memory ${m}`, r && `--memory-reservation ${r}`, cp && `--cpus ${cp}`, rp !== "no" && `--restart ${rp}`, "…"].filter(Boolean).join(" ");
  };
  for (const i of [mem, res, cpus, retries]) i.addEventListener("input", refresh);
  restart.addEventListener("change", refresh);

  apply.addEventListener("click", async () => {
    err.hidden = true;
    const body = {};
    if (mem.value.trim() && mem.value.trim() !== cur.memory) body.memory = mem.value.trim();
    if (res.value.trim() && res.value.trim() !== cur.reservation) body.memoryReservation = res.value.trim();
    if (cpus.value.trim() && cpus.value.trim() !== cur.cpus) body.cpus = cpus.value.trim();
    if (restart.value !== cur.restart || +retries.value !== cur.retries) { body.restart = restart.value; body.maxRetries = +retries.value || 0; }
    if (!Object.keys(body).length) { err.textContent = "Nothing has changed."; err.hidden = false; return; }
    const what = [body.memory && `memory limit ${body.memory}`, body.memoryReservation && `reservation ${body.memoryReservation}`,
      body.cpus && `${body.cpus} CPU${+body.cpus === 1 ? "" : "s"}`, body.restart && `restart ${body.restart}${body.restart === "on-failure" && body.maxRetries ? ` (${body.maxRetries} retries)` : ""}`].filter(Boolean).join(", ");
    const ok = await confirmBox(`Change ${name}?`, el("div",
      el("p", `Sets ${what}. It applies immediately to the running container, without a restart.`),
      service ? el("p", `This container belongs to Compose service "${service}". The next docker compose up recreates it with the compose file's settings, so this change is lost unless you put it in the compose file too (the snippet in the panel).`)
        : el("p.muted.small", "It lasts for the life of this container; a new container from the same image starts without it."),
      body.memory && c.State === "running" ? el("p.muted.small", "If the container already uses more memory than the new limit, the kernel reclaims or kills it.") : null),
    "Apply", false);
    if (!ok) return;
    apply.disabled = true;
    try {
      const r = await api("POST", `${P()}/containers/${encodeURIComponent(c.Id)}/limits`, body);
      toast(`${name}: limits applied${r.warnings?.length ? ` (${r.warnings.join("; ")})` : ""}`);
      loadAdvice(true).catch(() => { /* recommendations catch up on their own */ });
      Object.assign(cur, { memory: body.memory ?? cur.memory, reservation: body.memoryReservation ?? cur.reservation, cpus: body.cpus ?? cur.cpus,
        restart: body.restart ?? cur.restart, retries: body.restart ? body.maxRetries : cur.retries });
      refresh();
      if (state.tab === "containers") render();
    } catch (e) {
      err.textContent = e.message; err.hidden = false;
    } finally { apply.disabled = false; }
  });

  const row = (label, help, ...ctrls) => el("div.lim-row", el("div.lim-label", el("strong", label), el("span.muted.small", help)), el("div.lim-ctrl", ...ctrls));
  openDrawer(`Limits: ${name}`,
    el("div.card.lim-card",
      prefill ? el("p.small.lim-prefill", "The recommended values are filled in. Change them if you like, then Apply.") : null,
      el("p.muted.small", "Changes apply live with docker update: no restart. Sizes like 256m, 1g or 1.5g; CPUs like 0.5 or 2."),
      row("Memory limit", "The most it may use; above this the kernel kills it.", mem, presets(MEM_PRESETS, mem)),
      row("Memory reservation", "A soft floor kept for it when the VM runs short.", res, presets(["64m", "128m", "256m", "512m"], res)),
      row("CPUs", "Share of the VM's CPUs it may use.", cpus, presets(CPU_PRESETS, cpus)),
      row("Restart policy", "What Docker does when it exits or the VM restarts.", el("div.lim-inline", restart, el("label.check.small", "Retries", retries))),
      hint, err,
      el("div.lim-actions", apply)),
    el("div.card",
      el("div.card-head", el("h2", service ? "Keep it in the compose file" : "To keep it next time"),
        el("button.ghost.mini", { type: "button", onclick: async () => {
          try { await navigator.clipboard.writeText(snippet.textContent); toast("Copied"); } catch { toast("Copy failed: select the text instead", true); }
        } }, "Copy")),
      el("p.muted.small", service ? "docker compose up resets these settings from the compose file. Add this to it:" : "Settings changed here last for this container only. In a compose file:"),
      snippet,
      el("p.muted.small", "Or with docker run:"), runLine));
  refresh();
}

// ---------- logs ----------

// logQuery turns the search box into a test and a marker: plain text matches case-insensitively,
// /regex/flags is a regular expression (case-insensitive unless flags are given).
function logQuery(raw) {
  const q = raw.trim();
  if (!q) return { test: null, mark: "" };
  const m = /^\/(.+)\/([gimsuy]*)$/.exec(q);
  if (m) {
    try {
      const re = new RegExp(m[1], (m[2] || "i").replace("g", ""));
      return { test: (t) => re.test(t), mark: re };
    } catch (e) { return { test: null, mark: "", error: e.message }; }
  }
  const lower = q.toLowerCase();
  return { test: (t) => t.toLowerCase().includes(lower), mark: q };
}

function stampName(name) {
  const d = new Date();
  const p = (n) => String(n).padStart(2, "0");
  return `${name.replace(/[^\w.-]+/g, "_")}-${d.getFullYear()}${p(d.getMonth() + 1)}${p(d.getDate())}-${p(d.getHours())}${p(d.getMinutes())}.log`;
}

const LOG_CAP = 5000;

// logViewer opens the log drawer. opts: title, name (for downloads), path (the SSE endpoint,
// without query), project (lines carry service names and are kept in time order).
function logViewer(opts) {
  const log = el("pre.log");
  const search = el("input", { type: "search", placeholder: "Text or /regex/", "aria-label": "Search logs" });
  const modeBtns = ["Highlight", "Filter"].map((m) => el("button", { type: "button", "data-mode": m.toLowerCase() }, m));
  const seg = el("div.seg.seg-sm", { role: "group", "aria-label": "Search mode" }, modeBtns);
  const errs = el("input", { type: "checkbox" });
  const stamps = el("input", { type: "checkbox" });
  const follow = el("input", { type: "checkbox", checked: true });
  const since = el("select", { "aria-label": "Since" },
    [["", "All"], ["300", "Last 5 min"], ["3600", "Last hour"], ["86400", "Last 24 h"]].map(([v, t]) => el("option", { value: v }, t)));
  const count = el("span.muted.small.log-count");
  const status = el("span.small.log-status");
  let mode = safeGet("logmode") === "filter" ? "filter" : "highlight";
  stamps.checked = safeGet("logstamps") === "1";
  since.value = safeGet("logsince") || "";
  const buf = [];
  const services = new Map();
  let query = logQuery(""), ver = 0, lastTs = "", repaintT = null, shownN = 0;

  const svcClass = (n) => {
    if (!services.has(n)) services.set(n, services.size % 8);
    return `svc svc-${services.get(n)}`;
  };
  const lvl = (l) => (l.lvl === undefined ? (l.lvl = logfmt.level(l.t)) : l.lvl);
  const passes = (l) => (!errs.checked || lvl(l)) && (mode !== "filter" || !query.test || query.test(l.t));
  const node = (l) => {
    if (l.node && l.ver === ver) return l.node;
    const div = logfmt.line(stamps.checked && l.ts ? `${l.ts} ${l.t}` : l.t, query.mark, l.s === "stderr");
    if (l.n) {
      const tag = document.createElement("span");
      tag.className = svcClass(l.n);
      tag.textContent = l.n;
      div.prepend(tag, " ");
    }
    l.node = div; l.ver = ver;
    return div;
  };
  const counter = () => {
    const shown = shownN;
    count.textContent = shown === buf.length ? `${buf.length} line${buf.length === 1 ? "" : "s"}${buf.length >= LOG_CAP ? " (newest 5000 kept)" : ""}` : `${shown} of ${buf.length} lines`;
  };
  const paint = () => {
    clearTimeout(repaintT); repaintT = null;
    const shown = buf.filter(passes);
    shownN = shown.length;
    log.replaceChildren(...shown.map(node));
    if (!shown.length && buf.length) log.append(el("span.muted", "No lines match.\n"));
    log.scrollTop = log.scrollHeight;
    counter();
  };
  const restyle = () => { ver++; paint(); };
  const setMode = (m) => {
    mode = m; safeSet("logmode", m);
    for (const b of modeBtns) { const on = b.dataset.mode === m; b.classList.toggle("on", on); b.setAttribute("aria-pressed", on); }
    paint();
  };

  const add = (l) => {
    // keep time order across containers; a line older than the newest goes in its place
    if (opts.project && l.ts && l.ts < lastTs) {
      let lo = 0, hi = buf.length;
      while (lo < hi) { const mid = (lo + hi) >> 1; if ((buf[mid].ts || "") <= l.ts) lo = mid + 1; else hi = mid; }
      buf.splice(lo, 0, l);
      if (buf.length > LOG_CAP) buf.shift();
      repaintT ??= setTimeout(paint, 120);
      return;
    }
    if (l.ts) lastTs = l.ts;
    buf.push(l);
    if (buf.length > LOG_CAP) {
      const old = buf.shift();
      if (old.node?.isConnected) { old.node.remove(); shownN--; }
    }
    if (repaintT || !passes(l)) { counter(); return; }
    const atBottom = log.scrollTop + log.clientHeight >= log.scrollHeight - 30;
    log.querySelector(":scope > span.muted")?.remove();
    log.append(node(l));
    shownN++;
    if (atBottom) log.scrollTop = log.scrollHeight;
    counter();
  };

  const start = () => {
    if (state.logSource) state.logSource.close();
    buf.length = 0; services.clear(); lastTs = ""; ver++; shownN = 0;
    log.replaceChildren();
    counter();
    const sinceV = since.value;
    const qs = new URLSearchParams({ tail: sinceV ? String(LOG_CAP) : "500", follow: follow.checked ? "1" : "0" });
    if (sinceV) qs.set("since", sinceV);
    const src = new EventSource(`${opts.path}?${qs}`);
    state.logSource = src;
    status.textContent = follow.checked ? "following" : "loading";
    status.className = "small log-status on";
    src.addEventListener("services", (e) => { for (const n of JSON.parse(e.data)) svcClass(n); });
    src.addEventListener("line", (e) => add(JSON.parse(e.data)));
    const end = (text, bad) => {
      src.close();
      status.textContent = text;
      status.className = `small log-status${bad ? " bad" : ""}`;
    };
    src.addEventListener("done", () => end(follow.checked ? "stream ended" : "loaded"));
    src.addEventListener("failed", (e) => { end("failed", true); log.append(el("span.err", JSON.parse(e.data) + "\n")); });
    // never let EventSource reconnect on its own: it would replay the tail as duplicates
    src.onerror = () => { if (src.readyState !== EventSource.CLOSED) end("disconnected", true); };
  };

  search.addEventListener("input", () => {
    query = logQuery(search.value);
    search.classList.toggle("invalid", !!query.error);
    search.title = query.error || "";
    restyle();
  });
  for (const b of modeBtns) b.addEventListener("click", () => setMode(b.dataset.mode));
  errs.addEventListener("change", paint);
  stamps.addEventListener("change", () => { safeSet("logstamps", stamps.checked ? "1" : "0"); restyle(); });
  follow.addEventListener("change", start);
  since.addEventListener("change", () => { safeSet("logsince", since.value); start(); });

  const dl = () => {
    const rows = buf.filter(passes);
    const text = rows.map((l) => `${l.ts || "-"} ${l.n || opts.name}${l.s === "stderr" ? " stderr" : ""} | ${l.t}`).join("\n") + "\n";
    download(stampName(opts.name), text);
  };
  openDrawer(opts.title,
    el("div.log-bar",
      el("div.toolbar", seg, search, el("label.check", errs, "Errors and warnings only")),
      el("div.toolbar", el("label.check", stamps, "Timestamps"), el("label.check", follow, "Follow"), since,
        el("button.ghost.mini", { type: "button", onclick: dl, title: "Downloads the lines shown, with timestamps and source names" }, "Download"),
        opts.link ? linkBtn(...opts.link) : null,
        el("span.log-meta", status, count))),
    log);
  setMode(mode);
  start();
}

function showLogs(id, name) {
  logViewer({ title: `Logs: ${name}`, name, path: `${P()}/containers/${encodeURIComponent(id)}/logs`, link: ["containers", { name, open: "logs" }] });
}

function showProjectLogs(project) {
  logViewer({ title: `Logs: Compose ${project}`, name: project, project: true, path: `${P()}/projects/${encodeURIComponent(project)}/logs` });
}
