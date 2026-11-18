// Colima Dashboard: the Kubernetes console. Loaded after app.js and uses its helpers (el, api,
// confirmBox, toast, openDrawer, runJob, ...). Like app.js, it puts text on the page with
// textContent only, never innerHTML.
"use strict";

const K8S_SYSTEM = new Set(["kube-system", "kube-public", "kube-node-lease"]);
const K8S_VIEWS = [["overview", "Overview"], ["workloads", "Workloads"], ["pods", "Pods"], ["services", "Services"], ["events", "Events"], ["apply", "Apply YAML"], ["cluster", "Cluster"]];
const k8s = {
  view: safeGet("k8sView") || "overview",
  ns: safeGet("k8sNs") || "",
  sys: safeGet("k8sSys") === "1",
  podMode: safeGet("k8sPodMode") || "status",
  sevFilter: "",
  k: null, ov: null, wl: null, net: null, fwd: [],
  namespaces: new Set(),
  draft: "",
};
if (!K8S_VIEWS.some(([v]) => v === k8s.view)) k8s.view = "overview";

// openDrawer puts every argument on the page, null included; drop the empty ones first.
function openK8sDrawer(title, ...nodes) { openDrawer(title, ...nodes.filter((n) => n !== null && n !== undefined && n !== false)); }

// ---------- card heads, tones, empty states ----------

// Icons for the card heads, added to app.js's set (static SVG, no data from the cluster).
Object.assign(CHIP_PATHS, {
  alert: '<path d="M12 3 2 20h20z"/><path d="M12 10v4M12 17v.5"/>',
  node: '<rect x="3" y="4" width="18" height="7" rx="2"/><rect x="3" y="13" width="18" height="7" rx="2"/><path d="M7 7.5h.5M7 16.5h.5"/>',
  bell: '<path d="M6 16V11a6 6 0 1 1 12 0v5l2 2H4z"/><path d="M10 21h4"/>',
  pod: '<path d="M12 3 4 7.5v9L12 21l8-4.5v-9z"/><path d="M4 7.5 12 12l8-4.5M12 12v9"/>',
  layers: '<path d="m12 3 9 5-9 5-9-5z"/><path d="m3 13 9 5 9-5"/>',
  plug: '<circle cx="6" cy="12" r="2.5"/><circle cx="18" cy="6" r="2.5"/><circle cx="18" cy="18" r="2.5"/><path d="M8.3 11 15.7 7M8.3 13l7.4 4"/>',
  fwd: '<path d="M4 8h13M13 4l4 4-4 4"/><path d="M20 16H7M11 12l-4 4 4 4"/>',
  clock: '<circle cx="12" cy="12" r="9"/><path d="M12 7v5l3 2"/>',
  code: '<path d="m9 7-5 5 5 5M15 7l5 5-5 5"/>',
  term: '<rect x="3" y="4" width="18" height="16" rx="2"/><path d="m7 9 3 3-3 3M12 15h5"/>',
  check: '<circle cx="12" cy="12" r="9"/><path d="m8 12 3 3 5-6"/>',
});

// k8sHead is the General page's card head (coloured icon, title) with the count as a pill and
// anything else on the right.
function k8sHead(hue, icon, title, count, ...right) {
  return el(`div.info-head.k8s-head.${hue}`, chip(icon),
    el("h2", title, count !== null && count !== undefined ? el("span.pill.k8s-count", String(count)) : null),
    el("div.k8s-head-r", right));
}

function emptyState(icon, title, hint, tone) {
  return el(`div.k8s-empty${tone ? "." + tone : ""}`, chip(icon), el("div", el("strong", title), hint ? el("p.small.muted", hint) : null));
}

const SVC_TONE = { ClusterIP: "info", NodePort: "go", LoadBalancer: "purple", ExternalName: "warn" };
const KIND_TONE = { Deployment: "info", StatefulSet: "purple", DaemonSet: "go", ReplicaSet: "info", Job: "warn", CronJob: "warn" };

// ---------- formatting ----------

function cpuFmt(m) {
  if (!m) return "–";
  return m >= 1000 ? `${(m / 1000).toFixed(2).replace(/\.?0+$/, "")} CPU` : `${m}m`;
}
function memFmt(b) {
  if (!b) return "–";
  if (b >= 1073741824) return `${(b / 1073741824).toFixed(1).replace(/\.0$/, "")}Gi`;
  if (b >= 1048576) return `${Math.round(b / 1048576)}Mi`;
  if (b >= 1024) return `${Math.round(b / 1024)}Ki`;
  return `${b}B`;
}
const nsShown = (ns) => !ns || (k8s.ns ? ns === k8s.ns : (k8s.sys || !K8S_SYSTEM.has(ns)));
const kpath = (...parts) => parts.map(encodeURIComponent).join("/");
const SEV_LABEL = { fail: "Problem", warn: "Warning", info: "Tip" };
const EDITABLE = new Set(["Deployment", "StatefulSet", "DaemonSet"]);

function podTone(p) {
  if (p.reason === "Terminating") return "busy";
  if (p.reason) return "fail";
  const [r, t] = p.ready.split("/");
  if (p.phase === "Running" && r === t) return "ok";
  if (p.phase === "Succeeded") return "tone-info";
  return p.phase === "Failed" ? "fail" : "warn";
}

// A small bar of use against a limit (or the request when there is no limit).
function useBar(use, req, lim, fmt, known) {
  const base = lim || req;
  if (!known) return el("span.muted.small", "–");
  const pct = base ? Math.round(use / base * 100) : 0;
  const tone = !base ? "" : pct >= 90 ? "bad" : pct >= 70 ? "warn" : "";
  const fill = el(`span.${tone || "ok"}`);
  fill.style.width = `${Math.min(100, pct)}%`; // CSSOM, which the page's CSP allows
  const bar = el("span.rbar", fill);
  return el("span.rcell", { title: base ? `${fmt(use)} of its ${lim ? "limit" : "request"} of ${fmt(base)} (${pct}%)` : `${fmt(use)}; no request or limit to compare with` },
    el("span.rnum", fmt(use) + (base ? ` · ${pct}%` : "")), base ? bar : el("span.muted.small", lim ? "" : "no limit"));
}

// ---------- page ----------

async function renderKubernetes(main) {
  const head = el("div.head", el("div", el("h1", "Kubernetes"),
    el("p.muted", "k3s inside the Colima VM: health, resources, workloads, YAML and port forwards.")));
  if (!isRunning()) { main.replaceChildren(head, notRunningCard()); return; }
  main.replaceChildren(head, el("div.card", el("p.muted", "Loading…")));
  const k = await api("GET", `${P()}/kubernetes`);
  k8s.k = k;
  if (!k.enabled) { main.replaceChildren(head, k8sSettingsCard(k)); return; }

  const seg = el("div.seg.k8s-seg", { role: "tablist", "aria-label": "Kubernetes views" },
    K8S_VIEWS.map(([v, label]) => el(`button.${v === k8s.view ? "on" : "off"}`, { type: "button", role: "tab", "aria-selected": String(v === k8s.view), dataset: { view: v } }, label)));
  const nsSel = el("select", { "aria-label": "Namespace" });
  const sys = el("input", { type: "checkbox", checked: k8s.sys });
  const body = el("div.k8s-body");
  const fillNs = () => {
    const names = [...k8s.namespaces].sort();
    if (k8s.ns && !names.includes(k8s.ns)) names.unshift(k8s.ns);
    nsSel.replaceChildren(el("option", { value: "" }, "All namespaces"), ...names.map((n) => el("option", { value: n, selected: n === k8s.ns }, n)));
  };
  k8s.fillNs = fillNs;
  fillNs();
  const redraw = () => drawK8sView(body);
  seg.addEventListener("click", (e) => {
    const b = e.target.closest("button[data-view]");
    if (!b) return;
    k8s.view = b.dataset.view;
    safeSet("k8sView", k8s.view);
    for (const x of seg.children) { x.className = x === b ? "on" : "off"; x.setAttribute("aria-selected", String(x === b)); }
    redraw();
  });
  nsSel.addEventListener("change", () => { k8s.ns = nsSel.value; safeSet("k8sNs", k8s.ns); redraw(); });
  sys.addEventListener("change", () => { k8s.sys = sys.checked; safeSet("k8sSys", sys.checked ? "1" : "0"); redraw(); });
  main.replaceChildren(head,
    el("div.k8s-bar", seg, el("div.toolbar", nsSel, el("label.check.small", sys, "Show kube-system"),
      el("button.ghost.mini", { type: "button", onclick: redraw }, "Refresh"))),
    body);
  await drawK8sView(body);
  // Live views refresh themselves, but never under someone typing.
  setTimer(() => {
    if (!["overview", "pods", "workloads", "services", "events"].includes(k8s.view)) return;
    if (document.activeElement && body.contains(document.activeElement) && document.activeElement.matches("input, select, textarea")) return;
    if (!$("#drawer").hidden) return;
    drawK8sView(body, true);
  }, 15000, main);
}

function noteNamespaces(list) {
  const before = k8s.namespaces.size;
  for (const x of list || []) if (x.namespace) k8s.namespaces.add(x.namespace);
  if (k8s.namespaces.size !== before && k8s.fillNs) k8s.fillNs();
}

async function loadOverview() {
  k8s.ov = await api("GET", `${P()}/kubernetes/overview`);
  noteNamespaces(k8s.ov.pods);
  return k8s.ov;
}
async function loadWorkloads() {
  k8s.wl = await api("GET", `${P()}/kubernetes/workloads`);
  noteNamespaces(k8s.wl);
  return k8s.wl;
}
async function loadNetwork() {
  k8s.net = await api("GET", `${P()}/kubernetes/network`);
  noteNamespaces(k8s.net.services);
  return k8s.net;
}

async function drawK8sView(body, quiet) {
  const view = k8s.view;
  if (!quiet) body.replaceChildren(el("div.card", el("p.muted", "Loading…")));
  let nodes;
  try {
    nodes = await ({ overview: overviewView, workloads: workloadsView, pods: podsView, services: servicesView, events: eventsView, apply: applyView, cluster: clusterView })[view]();
  } catch (e) {
    nodes = [el("div.notice.bad", e.message)];
  }
  if (k8s.view !== view) return; // switched while loading
  body.replaceChildren(...nodes);
}

// ---------- Overview: issues, node, warnings ----------

async function overviewView() {
  const ov = await loadOverview();
  return [issuesCard(ov), ...ov.nodes.map(nodeCard), ov.nodesError ? el("div.notice.bad", ov.nodesError) : null, warningsCard(ov)].filter(Boolean);
}

function issuesCard(ov) {
  const all = ov.issues.filter((i) => nsShown(i.namespace));
  const count = (s) => all.filter((i) => i.severity === s).length;
  const shown = k8s.sevFilter ? all.filter((i) => i.severity === k8s.sevFilter) : all;
  const pods = ov.pods.filter((p) => nsShown(p.namespace)).length;
  const card = el("section.card.fix-card.k8s-issues");
  const seg = el("div.seg.small-seg", [["", `All ${all.length}`], ["fail", `Problems ${count("fail")}`], ["warn", `Warnings ${count("warn")}`], ["info", `Tips ${count("info")}`]]
    .map(([s, label]) => el(`button.${k8s.sevFilter === s ? "on" : "off"}`, { type: "button", onclick: () => { k8s.sevFilter = s; card.replaceWith(issuesCard(ov)); } }, label)));
  const recs = allRecs(all);
  const applyAll = recs.length ? el("button.mini", { type: "button", title: "Apply every resource recommendation that can be applied from here", onclick: () => applyAllRecs(recs) },
    `Apply all recommendations (${recs.length})`) : null;
  const checked = `Checked ${pods} pod${pods === 1 ? "" : "s"}${ov.metrics ? " and their live use" : ""}`;
  const head = k8sHead("t8", "alert", "Config issues", all.length, applyAll);
  if (!all.length) {
    card.append(head, emptyState("check", `No problems found in ${pods} pod${pods === 1 ? "" : "s"}`, `${checked}: requests and limits, probes, image tags, crashes and what fits on the node.`, "ok"));
    return card;
  }
  const items = shown.map(issueItem);
  const list = el("ul.fix-list", items.slice(0, 8));
  card.append(head, el("div.k8s-subbar", el("span.small.muted", `${checked}.`), seg), list);
  if (items.length > 8) {
    const more = el("button.ghost.mini", { type: "button" }, `Show ${items.length - 8} more`);
    more.addEventListener("click", () => { list.replaceChildren(...items); more.remove(); });
    card.append(el("div.k8s-more", more));
  }
  if (!ov.metrics && ov.metricsError) card.append(el("p.small.muted.k8s-more", ov.metricsError));
  return card;
}

function issueItem(is) {
  const pod = is.pod && (k8s.ov?.pods || []).find((p) => p.namespace === is.namespace && p.name === is.pod);
  const where = [];
  if (is.node) where.push(el("span", `Node ${is.node}`));
  if (is.namespace) where.push(el("span", is.namespace));
  if (is.workload) where.push(el("span", is.workload));
  if (is.pod) where.push(pod ? el("button.link", { type: "button", onclick: () => openPod(pod), title: "Pod details and events" }, is.pod) : el("span", is.pod));
  if (is.container) where.push(el("span", `container ${is.container}`));
  const acts = [];
  if (is.plan) acts.push(el("button.mini", { type: "button", onclick: () => openFix(is) }, fixLabel(is)));
  if (is.action === "previous-logs" && pod) acts.push(el("button.ghost.mini", { type: "button", onclick: () => showPodLogs(pod, true) }, "Logs of the last run"));
  if (pod) acts.push(el("button.ghost.mini", { type: "button", onclick: () => openPod(pod) }, "Details"));
  return el(`li.fix-item.${is.severity}`,
    el("div.fix-title", el("strong", is.title), el(`span.sev.${is.severity}`, SEV_LABEL[is.severity] || is.severity)),
    el("div.k8s-where.small.muted", where),
    el("p.fix-detail", is.detail),
    el("p.k8s-fix", el("strong", "Fix: "), is.fix),
    acts.length ? el("div.toolbar", acts) : null);
}

function fixLabel(is) {
  const pl = is.plan;
  return pl.kind === "resources" && pl.applicable ? "Fix…" : "Fix (how to)…";
}

// The worst issue of a pod, for the Fix button on its row.
function worstIssue(p) {
  const list = (k8s.ov?.issues || []).filter((i) => i.namespace === p.namespace && i.pod === p.name && i.plan && i.severity !== "info");
  return list.find((i) => i.severity === "fail") || list[0] || null;
}

const RES_FIELDS = [["cpuReq", "CPU request"], ["cpuLim", "CPU limit"], ["memReq", "Memory request"], ["memLim", "Memory limit"]];
const autoFix = (is) => is.plan && is.plan.kind === "resources" && is.plan.applicable && EDITABLE.has((is.workload || "").split("/")[0]);

// The larger of two quantities of one field: two issues on one container (say no memory limit
// and memory near its limit) both raise it, and the bigger answer covers both.
function biggerQ(f, a, b) {
  if (!a) return b;
  if (!b) return a;
  const n = f.startsWith("cpu") ? cpuMilli : memBytes;
  return n(b) > n(a) ? b : a;
}

// workloadRecs merges every auto-applicable recommendation for a workload, per container:
// {kind, ns, name, containers: [{name, cur, rec}], issues}. Values are as the server writes them.
function workloadRecs(ns, workload) {
  const issues = (k8s.ov?.issues || []).filter((i) => i.namespace === ns && i.workload === workload && autoFix(i));
  const byC = new Map();
  for (const is of issues) {
    const pl = is.plan;
    const c = byC.get(pl.container) || { name: pl.container, cur: { ...pl.current }, rec: { ...pl.current } };
    for (const [f] of RES_FIELDS) if ((pl.recommended[f] || "") !== (pl.current[f] || "")) c.rec[f] = biggerQ(f, c.rec[f] === c.cur[f] ? "" : c.rec[f], pl.recommended[f]);
    byC.set(pl.container, c);
  }
  for (const c of byC.values()) { // a raised request never ends above its limit
    if (c.rec.cpuLim && biggerQ("cpu", c.rec.cpuReq, c.rec.cpuLim) !== c.rec.cpuLim) c.rec.cpuLim = c.rec.cpuReq;
    if (c.rec.memLim && biggerQ("mem", c.rec.memReq, c.rec.memLim) !== c.rec.memLim) c.rec.memLim = c.rec.memReq;
  }
  const [kind, name] = workload.split("/");
  return { kind, ns, name, workload, containers: [...byC.values()], issues };
}

// recTable is current against recommended, a row per container and field; changed rows stand out.
function recTable(containers) {
  return el("div.table-wrap", el("table.k8s-table.k8s-compare",
    el("thead", el("tr", el("th", "Container"), el("th", "Setting"), el("th", "Now"), el("th", "Recommended"))),
    el("tbody", containers.flatMap((c) => RES_FIELDS.map(([f, label]) => {
      const a = c.cur[f] || "", b = c.rec[f] || "";
      return el(a !== b ? "tr.k8s-changed" : "tr.k8s-same", el("td", c.name), el("td", label), el("td.mono", a || "not set"), el("td.mono", b || "not set"));
    })))));
}

function recChanges(containers) {
  return containers.flatMap((c) => RES_FIELDS.filter(([f]) => (c.rec[f] || "") !== (c.cur[f] || ""))
    .map(([f, label]) => `${c.name}: ${label.toLowerCase()} ${c.cur[f] || "not set"} → ${c.rec[f]}`));
}

// runJobWait starts a job, shows it in the output panel and resolves with whether it worked.
async function runJobWait(path, body) {
  const job = await api("POST", path, body);
  return new Promise((resolve) => followJob(job, (ok) => resolve(ok)));
}

function applyRecsJob(r) {
  return runJobWait(`${P()}/kubernetes/workloads/${kpath(r.kind.toLowerCase(), r.ns, r.name)}/resources`,
    { containers: r.containers.map((c) => ({ name: c.name, ...Object.fromEntries(RES_FIELDS.map(([f]) => [f, c.rec[f] || ""])) })), dryRun: false });
}

// applyRecs applies one workload's recommendations after a confirm; the job panel shows the rollout.
async function applyRecs(r) {
  const body = el("div", el("p", "Pods restart one at a time with the new values; the output panel follows the rollout."),
    el("ul.k8s-changes", recChanges(r.containers).map((s) => el("li", s))));
  if (!await confirmBox(`Apply to ${r.workload}?`, body, "Apply recommendations", false)) return;
  closeDrawer();
  let ok = false;
  try { ok = await applyRecsJob(r); } catch (e) { toast(e.message, true); return; }
  toast(ok ? `Applied to ${r.workload}; its pods have rolled` : `${r.workload}: the change was made but the rollout did not finish; see the output`, !ok);
  refreshK8s();
}

// applyAllRecs applies every auto-applicable recommendation, one workload after another.
async function applyAllRecs(list) {
  const body = el("div", el("p", `${list.length} workload${list.length === 1 ? "" : "s"} change. Each one's pods restart one at a time, and the next starts when its rollout ends.`),
    el("ul.k8s-changes", list.map((r) => el("li", el("strong", `${r.ns}/${r.workload}`), el("ul", recChanges(r.containers).map((s) => el("li", s)))))));
  if (!await confirmBox("Apply all recommendations?", body, `Apply to ${list.length} workload${list.length === 1 ? "" : "s"}`, false)) return;
  let good = 0;
  for (const r of list) {
    try { if (await applyRecsJob(r)) good++; } catch (e) { toast(`${r.workload}: ${e.message}`, true); }
  }
  toast(good === list.length ? `Applied all ${good}; every rollout finished` : `${good} of ${list.length} rolled out; see the output for the rest`, good !== list.length);
  refreshK8s();
}

function allRecs(issues) {
  const seen = new Set();
  const out = [];
  for (const is of issues) {
    if (!autoFix(is)) continue;
    const key = `${is.namespace}|${is.workload}`;
    if (seen.has(key)) continue;
    seen.add(key);
    const r = workloadRecs(is.namespace, is.workload);
    if (recChanges(r.containers).length) out.push(r);
  }
  return out;
}

// openFix: the issue, then current against recommended, then one button that applies it all
// (the editor for adjusting the values is folded away below). What cannot be applied from here
// shows the same table with the YAML to copy, or the steps to take.
async function openFix(is) {
  const pl = is.plan;
  const pod = is.pod && (k8s.ov?.pods || []).find((p) => p.namespace === is.namespace && p.name === is.pod);
  const [wkind, wname] = (is.workload || "").split("/");
  const box = el("div.notice.k8s-why", el("strong", is.title), el("p.small", is.detail), el("p.small", el("strong", "Why this: "), pl.why));
  const vm = pl.vm ? el("p.small", `The Colima VM has ${pl.vm.haveCPUs} CPUs and ${pl.vm.haveGB} GB; ${pl.vm.cpus} CPUs and ${pl.vm.memoryGB} GB would fit. `,
    el("a", { href: "#general", onclick: () => closeDrawer() }, "Change the VM's size on General")) : null;
  const steps = pl.steps?.length ? el("ul.k8s-steps", pl.steps.map((s) => el("li", s))) : null;
  if (autoFix(is)) {
    const r = workloadRecs(is.namespace, is.workload);
    const w = await findWorkload(wkind, is.namespace, wname);
    const go = el("button.k8s-apply-recs", { type: "button", onclick: () => applyRecs(r) }, "Apply recommendations");
    const others = r.issues.length > 1 ? el("p.small.muted", `Covers ${r.issues.length} issues of ${r.workload}: ${[...new Set(r.issues.map((i) => i.title))].join("; ")}.`) : null;
    const editor = w ? el("details.k8s-adjust", el("summary", "Adjust values yourself"),
      el("div.k8s-adjust-body", resourcesEditor(w, { prefill: Object.fromEntries(r.containers.map((c) => [c.name, Object.fromEntries(RES_FIELDS.map(([f]) => [f, c.rec[f] || ""]).filter(([f, v]) => v !== (c.cur[f] || "")))])) }))) : null;
    openK8sDrawer(`Fix: ${r.workload}`, box, recTable(r.containers), el("div.toolbar", go, others), vm, steps, editor);
    go.focus();
    return;
  }
  const table = pl.current && pl.recommended ? recTable([{ name: pl.container, cur: pl.current, rec: pl.recommended }]) : null;
  const acts = el("div.toolbar",
    pl.patch ? copyBtn(pl.patch) : null,
    EDITABLE.has(wkind) ? el("button.ghost.mini", { type: "button", onclick: () => openYAML(wkind.toLowerCase(), is.namespace, wname) }, `Edit ${wkind} YAML`)
      : pod ? el("button.ghost.mini", { type: "button", onclick: () => openYAML("pod", is.namespace, pod.name) }, "YAML") : null,
    pod && pod.restarts ? el("button.ghost.mini", { type: "button", onclick: () => showPodLogs(pod, true) }, "Logs of the last run") : null,
    pod ? el("button.ghost.mini", { type: "button", onclick: () => openPod(pod) }, "Details and events") : null);
  openK8sDrawer(`Fix: ${is.title}`, box, table,
    pl.patch ? el("h3", pl.kind === "resources" ? "The change, for the manifest it came from" : "YAML to add (a strategic merge patch)") : null,
    pl.patch ? el("pre.log.k8s-patch", pl.patch) : null,
    acts, vm, steps);
}

async function findWorkload(kind, ns, name) {
  try {
    const hit = (list) => (list || []).find((x) => x.kind === kind && x.namespace === ns && x.name === name);
    return hit(k8s.wl) || hit(await loadWorkloads());
  } catch (e) { toast(e.message, true); return null; }
}

// meter is one row of a node card: what is used or booked against what the node has.
function meter(label, value, total, fmt, hint) {
  const pct = total ? Math.round(value / total * 100) : 0;
  const tone = pct > 100 ? "over" : pct >= 90 ? "bad" : pct >= 70 ? "warn" : "";
  const fill = el(`div.fill${tone ? "." + tone : ""}`);
  fill.style.width = `${Math.min(100, pct)}%`;
  return el("div.bar.k8s-meter", { title: hint || "" },
    el("span", label), el("div.track", fill),
    el("span.num", `${fmt(value)} · ${pct}%`, pct > 100 ? el("span.sev.fail.k8s-over", "over") : null));
}

function nodeCard(n) {
  const left = el("div.k8s-meters",
    el("h3", `CPU · ${cpuFmt(n.cpuAlloc)}`),
    meter("Requested", n.cpuReq, n.cpuAlloc, cpuFmt, "CPU the pods on this node reserve (requests). The scheduler fills a node up to 100% of this."),
    meter("Limits", n.cpuLim, n.cpuAlloc, cpuFmt, "The most CPU the pods may use between them (limits). Over 100% is overcommitment: fine for CPU, which is shared out when busy."),
    n.hasUsage ? meter("In use", n.cpuUse, n.cpuAlloc, cpuFmt, "What the node uses now, from metrics-server.") : null);
  const right = el("div.k8s-meters",
    el("h3", `Memory · ${memFmt(n.memAlloc)}`),
    meter("Requested", n.memReq, n.memAlloc, memFmt, "Memory the pods on this node reserve (requests)."),
    meter("Limits", n.memLim, n.memAlloc, memFmt, "The most memory the pods may use between them. Over 100% means the node runs out if several grow at once."),
    n.hasUsage ? meter("In use", n.memUse, n.memAlloc, memFmt, "What the node uses now, including the system and k3s itself.") : null);
  const over = n.cpuLim > n.cpuAlloc || n.memLim > n.memAlloc;
  return el("section.card.info-card.k8s-card2",
    k8sHead("t1", "node", `Node ${n.name}`, null,
      over ? el("span.pill.warn", { title: "Pods may use more than the node has if they all hit their limits" }, "overcommitted") : null,
      el(`span.pill.${n.ready ? "ok" : "fail"}`, n.ready ? "Ready" : "Not ready")),
    el("p.small.muted.k8s-sub", `${n.version} · ${n.os} · ${n.pods} of ${n.podsCap} pods · capacity ${cpuFmt(n.cpuCap)}, ${memFmt(n.memCap)}`),
    el("div.k8s-node", left, right),
    n.hasUsage ? null : el("p.small.muted", "Live use needs metrics-server."));
}

function warningsCard(ov) {
  const evs = ov.warnings.filter((e) => nsShown(e.namespace) && Date.now() / 1000 - e.last < 86400);
  const head = k8sHead("t4", "bell", "Warnings", evs.length, el("span.muted.small", "last day"),
    el("button.ghost.mini", { type: "button", onclick: () => switchView("events") }, "Events"));
  if (!evs.length) return el("section.card.info-card.k8s-card2", head, emptyState("bell", "No warning events in the last day", "Kubernetes keeps events for an hour; this shows what it still has.", "ok"));
  return el("section.card.info-card.k8s-card2", head, el("ul.k8s-events", evs.slice(0, 6).map(eventRow)));
}

function eventRow(e) {
  const pod = e.kind === "Pod" && (k8s.ov?.pods || []).find((p) => p.namespace === e.namespace && p.name === e.name);
  return el("li",
    el("span.k8s-ev-when.small.muted", ago(e.last)),
    el(`span.pill.${e.type === "Warning" ? "warn" : "x"}`, e.reason || e.type),
    el("span.k8s-ev-obj.small", pod ? el("button.link", { type: "button", onclick: () => openPod(pod) }, `${e.kind}/${e.name}`) : `${e.kind}/${e.name}`,
      e.namespace && !k8s.ns ? el("span.muted", ` · ${e.namespace}`) : null),
    el("span.k8s-ev-msg.small", e.message, e.count > 1 ? el("span.muted", ` ×${e.count}`) : null));
}

function switchView(v) {
  const b = document.querySelector(`.k8s-seg button[data-view="${v}"]`);
  if (b) b.click();
}

// ---------- Pods ----------

async function podsView() {
  const ov = await loadOverview();
  const pods = ov.pods.filter((p) => nsShown(p.namespace));
  const mode = el("div.seg.small-seg", [["status", "Status"], ["resources", "Resources"]].map(([m, label]) =>
    el(`button.${k8s.podMode === m ? "on" : "off"}`, { type: "button", onclick: () => { k8s.podMode = m; safeSet("k8sPodMode", m); drawK8sView($(".k8s-body"), true); } }, label)));
  const card = el("section.card.info-card.k8s-card2", k8sHead("t3", "pod", "Pods", pods.length, mode));
  if (!pods.length) {
    card.append(ov.pods.length ? emptyState("pod", "No pods with this filter", "kube-system is hidden unless you tick Show kube-system.")
      : emptyState("pod", "No pods yet", "Apply a manifest under Apply YAML and it shows up here."));
    return [card];
  }
  if (k8s.podMode === "resources") {
    if (!ov.metrics) card.append(el("div.notice", ov.metricsError || "Live use is not available."));
    card.append(el("div.table-wrap", el("table.k8s-table",
      el("thead", el("tr", ["Pod", "CPU request", "CPU limit", "CPU in use", "Memory request", "Memory limit", "Memory in use"].map((h) => el("th", h)))),
      shortBody(pods.map((p) => el("tr",
        el("td", podNameCell(p), fixButton(p)),
        el("td.num", cpuFmt(p.cpuReq)), el("td.num", cpuFmt(p.cpuLim)),
        el("td", useBar(p.cpuUse, p.cpuReq, p.containers.every((c) => c.cpuLim) ? p.cpuLim : 0, cpuFmt, p.hasUsage)),
        el("td.num", memFmt(p.memReq)), el("td.num", memFmt(p.memLim)),
        el("td", useBar(p.memUse, p.memReq, p.containers.every((c) => c.memLim) ? p.memLim : 0, memFmt, p.hasUsage)))), 40))),
    el("p.small.muted", "Sums over each pod's containers. The bar compares use with the limit, or with the request when a container has no limit."));
    return [card];
  }
  card.append(el("div.table-wrap", el("table.k8s-table",
    el("thead", el("tr", el("th", "Pod"), el("th", "Status"), el("th", "Ready"), el("th", "Restarts"), el("th", "CPU"), el("th", "Memory"), el("th", "Age"), el("th"))),
    shortBody(pods.map((p) => el("tr",
      el("td", podNameCell(p)),
      el("td", el(`span.pill.${podTone(p)}`, p.reason || p.phase)),
      el("td.small.num", p.ready),
      el("td.small.num", p.restarts ? el("span.bad-text", String(p.restarts)) : "0"),
      el("td.small.num", p.hasUsage ? cpuFmt(p.cpuUse) : "–"),
      el("td.small.num", p.hasUsage ? memFmt(p.memUse) : "–"),
      el("td.small.nowrap", ago(p.created)),
      el("td.actions",
        fixButton(p),
        el("button.ghost.mini", { type: "button", onclick: () => showPodLogs(p) }, "Logs"),
        p.phase === "Running" ? el("button.ghost.mini", { type: "button", onclick: () => podShell(p) }, "Shell") : null,
        el("button.ghost.mini.danger", { type: "button", onclick: () => deletePod(p) }, "Delete")))), 40))));
  return [card];
}

function fixButton(p) {
  const is = worstIssue(p);
  return is ? el(`button.mini.k8s-fixbtn.${is.severity}`, { type: "button", title: is.title, onclick: () => openFix(is) }, "Fix") : null;
}

function podNameCell(p) {
  return [el("button.link.mono.k8s-name", { type: "button", onclick: () => openPod(p), title: "Details, events, YAML" }, p.name),
    el("div.muted.small", [k8s.ns ? null : p.namespace, p.workload].filter(Boolean).join(" · "))];
}

// The pod drawer: containers with resources and probes, the issues found, events, and actions.
async function openPod(p) {
  const issues = (k8s.ov?.issues || []).filter((i) => i.namespace === p.namespace && i.pod === p.name);
  const events = el("div", el("p.muted.small", "Loading events…"));
  const kv = el("dl.kv",
    el("dt", "Status"), el("dd", el(`span.pill.${podTone(p)}`, p.reason || p.phase), ` ${p.ready} ready, ${p.restarts} restarts`),
    el("dt", "Namespace"), el("dd", p.namespace),
    p.workload ? el("dt", "Owner") : null, p.workload ? el("dd", p.workload) : null,
    el("dt", "Node"), el("dd", p.node || "not scheduled"),
    p.ip ? el("dt", "Pod IP") : null, p.ip ? el("dd.mono", p.ip) : null,
    p.qos ? el("dt", "QoS class") : null, p.qos ? el("dd", p.qos) : null,
    el("dt", "Created"), el("dd", ago(p.created)));
  const containers = el("div.table-wrap", el("table.k8s-table",
    el("thead", el("tr", ["Container", "State", "Requests", "Limits", "In use", "Probes"].map((h) => el("th", h)))),
    el("tbody", p.containers.map((c) => el("tr",
      el("td", el("strong", c.name), el("div.muted.small.mono", c.image)),
      el("td", el(`span.pill.${c.state === "running" && c.ready ? "ok" : c.reason && c.reason !== "Completed" ? "fail" : "warn"}`, c.reason || c.state || "unknown"),
        c.restarts ? el("div.small.muted", `${c.restarts} restarts`) : null,
        c.lastReason ? el("div.small.muted", `last run: ${c.lastReason}, exit ${c.lastExit}`) : null),
      el("td.small.nowrap", `${cpuFmt(c.cpuReq)} · ${memFmt(c.memReq)}`),
      el("td.small.nowrap", `${cpuFmt(c.cpuLim)} · ${memFmt(c.memLim)}`),
      el("td.small.nowrap", p.hasUsage ? `${cpuFmt(c.cpuUse)} · ${memFmt(c.memUse)}` : "–"),
      el("td.small", [c.readiness ? "readiness" : null, c.liveness ? "liveness" : null].filter(Boolean).join(", ") || el("span.bad-text", "none")))))));
  const [wkind, wname] = (p.workload || "").split("/");
  const acts = el("div.toolbar",
    el("button.ghost.mini", { type: "button", onclick: () => showPodLogs(p) }, "Logs"),
    p.restarts ? el("button.ghost.mini", { type: "button", onclick: () => showPodLogs(p, true) }, "Logs of the last run") : null,
    p.phase === "Running" ? el("button.ghost.mini", { type: "button", onclick: () => podShell(p) }, "Shell") : null,
    p.phase === "Running" ? el("button.ghost.mini", { type: "button", onclick: () => forwardDialog("pod", p.namespace, p.name, p.containers.flatMap((c) => c.ports)) }, "Port forward…") : null,
    el("button.ghost.mini", { type: "button", onclick: () => openYAML("pod", p.namespace, p.name) }, "YAML"),
    EDITABLE.has(wkind) ? el("button.ghost.mini", { type: "button", onclick: () => editResourcesByName(wkind, p.namespace, wname) }, `Edit ${wkind} resources`) : null,
    el("button.ghost.mini.danger", { type: "button", onclick: () => deletePod(p, closeDrawer) }, "Delete"));
  openK8sDrawer(`Pod ${p.name}`, acts, kv,
    p.schedMsg ? el("div.notice", el("strong", "Not scheduled: "), p.schedMsg) : null,
    el("h3", "Containers"), containers,
    issues.length ? el("h3", `Issues · ${issues.length}`) : null,
    issues.length ? el("ul.fix-list.k8s-drawer-issues", issues.map(issueItem)) : null,
    el("h3", "Events"), events);
  try {
    const evs = await api("GET", `${P()}/kubernetes/events?ns=${encodeURIComponent(p.namespace)}&name=${encodeURIComponent(p.name)}&kind=Pod`);
    events.replaceChildren(evs.length ? el("ul.k8s-events", evs.map(eventRow)) : el("p.muted.small", "No events. Kubernetes keeps them for an hour."));
  } catch (e) {
    events.replaceChildren(el("div.notice.bad", e.message));
  }
}

function showPodLogs(p, previous) {
  const log = el("pre.log");
  const follow = el("input", { type: "checkbox", checked: !previous });
  const prev = el("input", { type: "checkbox", checked: !!previous });
  const lines = [];
  const start = () => {
    if (state.logSource) state.logSource.close();
    lines.length = 0;
    log.replaceChildren();
    const src = new EventSource(`${P()}/kubernetes/pods/${kpath(p.namespace, p.name)}/logs?tail=500&follow=${follow.checked ? 1 : 0}&previous=${prev.checked ? 1 : 0}`);
    state.logSource = src;
    src.addEventListener("line", (e) => {
      const l = JSON.parse(e.data);
      lines.push(l.t);
      const atBottom = log.scrollTop + log.clientHeight >= log.scrollHeight - 30;
      log.append(lineNode(l, ""));
      if (log.childNodes.length > 5000) log.firstChild.remove();
      if (atBottom) log.scrollTop = log.scrollHeight;
    });
    src.addEventListener("done", () => { src.close(); if (!lines.length) log.append(el("span.muted", "No output.\n")); });
    src.addEventListener("failed", (e) => { src.close(); log.append(el("span.err", JSON.parse(e.data) + "\n")); });
  };
  follow.addEventListener("change", start);
  prev.addEventListener("change", start);
  openK8sDrawer(`Logs: ${p.namespace}/${p.name}`,
    el("div.toolbar", el("label.check", follow, "Follow"), el("label.check", prev, "Previous run (after a crash)"),
      el("button.ghost.mini", { type: "button", onclick: () => download(`${p.name}.log`, lines.join("\n")) }, "Download")),
    log);
  start();
}

async function deletePod(p, after) {
  if (!await confirmBox(`Delete pod ${p.name}?`, p.owner ? `${p.owner} starts a new one straight away, so this is a restart.` : "Nothing owns this pod, so it is gone for good.", "Delete", true)) return;
  try {
    await api("DELETE", `${P()}/kubernetes/pods/${kpath(p.namespace, p.name)}`);
    toast(`Deleting ${p.name}`);
    if (after) after();
    setTimeout(() => { const b = $(".k8s-body"); if (b) drawK8sView(b, true); }, 800);
  } catch (e) { toast(e.message, true); }
}

// A shell in one of the pod's containers; with several, pick which first.
async function podShell(p) {
  let c = p.containers[0]?.name;
  if (p.containers.length > 1) {
    const sel = el("select", { "aria-label": "Container" }, p.containers.map((x) => el("option", { value: x.name }, `${x.name} (${x.image})`)));
    if (!await confirmBox(`Shell in ${p.name}`, el("label.field", el("span", "Container"), sel), "Open shell", false)) return;
    c = sel.value;
  }
  showShell(c, p.containers.length > 1 ? `${p.name}/${c}` : p.name, { url: `${P()}/kubernetes/pods/${kpath(p.namespace, p.name)}/shell`, body: { container: c } });
}

// ---------- Workloads ----------

async function workloadsView() {
  const wls = (await loadWorkloads()).filter((w) => nsShown(w.namespace));
  const card = el("section.card.info-card.k8s-card2", k8sHead("t7", "layers", "Workloads", wls.length,
    el("span.small.muted", "Deployments, StatefulSets and DaemonSets")));
  if (!wls.length) { card.append(emptyState("layers", "No workloads with this filter", "Apply a Deployment under Apply YAML, or tick Show kube-system.")); return [card]; }
  card.append(el("div.table-wrap", el("table.k8s-table",
    el("thead", el("tr", ["Workload", "Ready", "Up to date", "Age", "Scale", ""].map((h) => el("th", h)))),
    shortBody(wls.map(workloadRow), 40))));
  return [card];
}

function workloadRow(w) {
  const kind = w.kind.toLowerCase();
  const tone = w.ready === w.desired && w.desired > 0 ? "ok" : w.desired === 0 ? "x" : w.ready === 0 ? "fail" : "warn";
  let scale = el("span.muted.small", "one per node");
  if (w.kind !== "DaemonSet") {
    const n = el("input", { type: "number", min: 0, max: 50, value: w.desired, "aria-label": `Replicas of ${w.name}` });
    const go = el("button.mini", { type: "button", disabled: true }, "Scale");
    const step = (d) => { n.value = Math.max(0, Math.min(50, (Number(n.value) || 0) + d)); go.disabled = Number(n.value) === w.desired; };
    n.addEventListener("input", () => { go.disabled = Number(n.value) === w.desired || n.value === ""; });
    go.addEventListener("click", async () => {
      const to = Number(n.value);
      if (!Number.isInteger(to) || to < 0 || to > 50) { toast("Replicas must be 0 to 50", true); return; }
      if (to === 0 && !await confirmBox(`Scale ${w.name} to 0?`, "Every pod of it stops until you scale it up again.", "Scale to 0", true)) return;
      runJob("POST", `${P()}/kubernetes/workloads/${kpath(kind, w.namespace, w.name)}/scale`, { replicas: to }, () => refreshK8s());
    });
    scale = el("div.scale",
      el("button.ghost.mini", { type: "button", "aria-label": "One fewer", onclick: () => step(-1) }, "−"), n,
      el("button.ghost.mini", { type: "button", "aria-label": "One more", onclick: () => step(1) }, "+"), go);
  }
  return el("tr",
    el("td", el(`span.pill.k8s-kind.tone-${KIND_TONE[w.kind] || "info"}`, w.kind), " ", el("button.link.k8s-name", { type: "button", onclick: () => openYAML(kind, w.namespace, w.name), title: "View and edit YAML" }, w.name),
      el("div.muted.small", [k8s.ns ? null : w.namespace, w.images.join(", ")].filter(Boolean).join(" · ")),
      w.message ? el("div.small.bad-text", w.message) : null),
    el("td", el(`span.pill.${tone}`, `${w.ready}/${w.desired}`)),
    el("td.small.num", String(w.updated)),
    el("td.small.nowrap", ago(w.created)),
    el("td", scale),
    el("td.actions.wrap",
      el("button.ghost.mini", { type: "button", onclick: () => restartWorkload(w) }, "Restart"),
      el("button.ghost.mini", { type: "button", onclick: () => rollbackWorkload(w) }, "Roll back"),
      el("button.ghost.mini", { type: "button", onclick: () => openResources(w) }, "Edit resources"),
      el("button.ghost.mini", { type: "button", onclick: () => openYAML(kind, w.namespace, w.name) }, "YAML")));
}

function refreshK8s() { const b = $(".k8s-body"); if (b) drawK8sView(b, true); }

async function restartWorkload(w) {
  const n = w.kind === "DaemonSet" ? "its pods" : `its ${w.desired} pod${w.desired === 1 ? "" : "s"}`;
  if (!await confirmBox(`Restart ${w.kind} ${w.name}?`, `Kubernetes replaces ${n} with fresh ones, a few at a time, so it keeps serving if it has more than one replica. The output shows the rollout.`, "Restart", false)) return;
  runJob("POST", `${P()}/kubernetes/workloads/${kpath(w.kind.toLowerCase(), w.namespace, w.name)}/restart`, undefined, () => refreshK8s());
}

async function rollbackWorkload(w) {
  const rev = w.revision ? ` It is on revision ${w.revision} now.` : "";
  if (!await confirmBox(`Roll back ${w.kind} ${w.name}?`, `Goes back to the previous version of its pod template (image, settings, resources), and the pods roll.${rev} Running it twice returns to where you started.`, "Roll back", true)) return;
  runJob("POST", `${P()}/kubernetes/workloads/${kpath(w.kind.toLowerCase(), w.namespace, w.name)}/undo`, undefined, () => refreshK8s());
}

// ---------- Resources editor ----------

const PRESETS = [
  ["Small", { cpuReq: "100m", memReq: "128Mi", cpuLim: "250m", memLim: "256Mi" }],
  ["Medium", { cpuReq: "250m", memReq: "256Mi", cpuLim: "500m", memLim: "512Mi" }],
  ["Large", { cpuReq: "500m", memReq: "512Mi", cpuLim: "1", memLim: "1Gi" }],
];
const CPU_RE = /^([1-9][0-9]{0,5}m|[0-9]{1,3}(\.[0-9]{1,3})?)$/;
const MEM_RE = /^[1-9][0-9]{0,6}(Ki|Mi|Gi|Ti|k|M|G|T)?$/;
function cpuMilli(v) { return !v ? 0 : v.endsWith("m") ? Number(v.slice(0, -1)) : Number(v) * 1000; }
function memBytes(v) {
  if (!v) return 0;
  const m = /^([0-9.]+)([A-Za-z]*)$/.exec(v);
  const mul = { "": 1, Ki: 1024, Mi: 1048576, Gi: 1073741824, Ti: 1099511627776, k: 1e3, M: 1e6, G: 1e9, T: 1e12 }[m ? m[2] : ""];
  return m && mul ? Number(m[1]) * mul : NaN;
}

async function editResourcesByName(kind, ns, name) {
  const w = await findWorkload(kind, ns, name);
  if (w) openResources(w);
  else toast(`${kind} ${name} was not found`, true);
}

function openResources(w) {
  openK8sDrawer(`Resources: ${w.kind} ${w.name}`, ...resourcesEditor(w));
}

// resourcesEditor edits a workload's requests and limits, with presets, a dry run and Apply.
// opts.prefill {container: {cpuReq, ...}} starts fields at a recommendation (the changed values).
function resourcesEditor(w, opts = {}) {
  const kind = w.kind.toLowerCase();
  const pods = (k8s.ov?.pods || []).filter((p) => p.namespace === w.namespace && p.workload === `${w.kind}/${w.name}`);
  const fields = ["cpuReq", "cpuLim", "memReq", "memLim"];
  const labels = { cpuReq: "CPU request", cpuLim: "CPU limit", memReq: "Memory request", memLim: "Memory limit" };
  const rows = w.containers.map((c) => {
    const cur = { cpuReq: c.requests.cpu || "", cpuLim: c.limits.cpu || "", memReq: c.requests.memory || "", memLim: c.limits.memory || "" };
    const pre = opts.prefill?.[c.name] || {};
    const inputs = {};
    for (const f of fields) inputs[f] = el("input", { value: pre[f] ?? cur[f], placeholder: f.startsWith("cpu") ? "e.g. 250m" : "e.g. 256Mi", "aria-label": `${labels[f]} for ${c.name}`, autocomplete: "off", spellcheck: "false" });
    // What it uses now, the most over its pods, as a guide.
    let use = null;
    const seen = pods.flatMap((p) => p.hasUsage ? p.containers.filter((x) => x.name === c.name) : []);
    if (seen.length) use = `In use now: up to ${cpuFmt(Math.max(...seen.map((x) => x.cpuUse))) || "0m"} CPU and ${memFmt(Math.max(...seen.map((x) => x.memUse)))} over ${seen.length} pod${seen.length === 1 ? "" : "s"}.`;
    const presets = el("div.toolbar", el("span.small.muted", "Presets:"), PRESETS.map(([name, v]) =>
      el("button.ghost.mini", { type: "button", title: `requests ${v.cpuReq} CPU, ${v.memReq}; limits ${v.cpuLim} CPU, ${v.memLim}`, onclick: () => { for (const f of fields) inputs[f].value = v[f]; update(); } }, name)),
    el("button.ghost.mini", { type: "button", title: "Put back what is set now", onclick: () => { for (const f of fields) inputs[f].value = cur[f]; update(); } }, "Reset"));
    return { c, cur, inputs, node: el("fieldset.k8s-res",
      el("legend", el("strong", c.name), el("span.muted.small.mono", ` ${c.image}`)),
      use ? el("p.small.muted", use) : null,
      el("div.res-grid", fields.map((f) => el("label.field", el("span", labels[f]), inputs[f]))),
      presets) };
  });
  const preview = el("div.k8s-preview");
  const out = el("pre.log.k8s-out", { hidden: true });
  const dry = el("button.ghost", { type: "button" }, "Check (dry run)");
  const apply = el("button", { type: "button" }, "Apply…");
  const collect = () => rows.map((r) => ({ name: r.c.name, ...Object.fromEntries(fields.map((f) => [f, r.inputs[f].value.trim()])) }));
  const validate = () => {
    for (const r of collect()) {
      for (const f of ["cpuReq", "cpuLim"]) if (r[f] && !CPU_RE.test(r[f])) return `${r.name}: ${labels[f].toLowerCase()} "${r[f]}" is not a CPU amount. Use millicores like 250m, or cores like 0.5.`;
      for (const f of ["memReq", "memLim"]) if (r[f] && !MEM_RE.test(r[f])) return `${r.name}: ${labels[f].toLowerCase()} "${r[f]}" is not a memory amount. Use Mi or Gi, like 256Mi or 1Gi.`;
      if (r.cpuReq && r.cpuLim && cpuMilli(r.cpuReq) > cpuMilli(r.cpuLim)) return `${r.name}: the CPU limit is below the request.`;
      if (r.memReq && r.memLim && memBytes(r.memReq) > memBytes(r.memLim)) return `${r.name}: the memory limit is below the request.`;
    }
    return "";
  };
  const changes = () => {
    const list = [];
    for (const r of rows) for (const f of fields) {
      const v = r.inputs[f].value.trim();
      if (v !== r.cur[f]) list.push(el("li", el("strong", r.c.name), ` ${labels[f].toLowerCase()}: `, el("span.mono", r.cur[f] || "not set"), " → ", el("span.mono", v || "not set")));
    }
    return list;
  };
  const update = () => {
    const err = validate();
    const ch = changes();
    out.hidden = true;
    preview.replaceChildren(err ? el("div.notice.bad", err)
      : ch.length ? el("div", el("strong", "What changes"), el("ul.k8s-changes", ch))
        : el("p.small.muted", "No changes yet. Type new amounts or pick a preset."));
    dry.disabled = apply.disabled = !!err || !ch.length;
  };
  for (const r of rows) for (const f of fields) r.inputs[f].addEventListener("input", update);
  const url = `${P()}/kubernetes/workloads/${kpath(kind, w.namespace, w.name)}/resources`;
  dry.addEventListener("click", async () => {
    dry.disabled = true;
    try {
      const r = await api("POST", url, { containers: collect(), dryRun: true });
      out.hidden = false; out.className = "log k8s-out"; out.textContent = `The API server accepts it (nothing saved):\n${r.output}`;
    } catch (e) {
      out.hidden = false; out.className = "log k8s-out bad"; out.textContent = e.message;
    }
    dry.disabled = false;
  });
  apply.addEventListener("click", async () => {
    const n = w.kind === "DaemonSet" ? "its pods" : `its ${w.desired} pod${w.desired === 1 ? "" : "s"}`;
    const body = el("div", el("p", `Kubernetes replaces ${n} with new ones that have these resources, a few at a time. Pods that no longer fit stay Pending.`), el("ul.k8s-changes", changes()));
    if (!await confirmBox(`Change resources of ${w.name}?`, body, "Apply and roll pods", false)) return;
    closeDrawer();
    runJob("POST", url, { containers: collect(), dryRun: false }, () => refreshK8s());
  });
  update();
  return [el("p.small.muted", "Requests are what the scheduler reserves for each container; limits are the most it may use. Over its memory limit a container is killed (OOMKilled); over its CPU limit it is slowed down. Leave a field empty to remove it."),
    ...rows.map((r) => r.node), preview, el("div.toolbar", dry, apply), out];
}

// ---------- Services, ingresses, port forwards ----------

async function servicesView() {
  const [net] = await Promise.all([loadNetwork(), loadForwards()]);
  const svcs = net.services.filter((s) => nsShown(s.namespace));
  const ings = net.ingresses.filter((i) => nsShown(i.namespace));
  const open = (port) => el("a", { href: `http://localhost:${port}`, target: "_blank", rel: "noopener noreferrer" }, `localhost:${port}`);
  const sCard = el("section.card.info-card.k8s-card2", k8sHead("t1", "plug", "Services", svcs.length, el("span.small.muted", "how pods are reached")));
  if (!svcs.length) sCard.append(emptyState("plug", "No services with this filter", "A Service gives a set of pods one address; apply one under Apply YAML."));
  else {
    sCard.append(el("div.table-wrap", el("table.k8s-table",
      el("thead", el("tr", ["Service", "Type", "Cluster IP", "Ports", "On your Mac", ""].map((h) => el("th", h)))),
      shortBody(svcs.map((s) => el("tr",
        el("td", el("strong", s.name), k8s.ns ? null : el("div.muted.small", s.namespace)),
        el("td", el(`span.pill.tone-${SVC_TONE[s.type] || "info"}`, s.type)),
        el("td.mono.small", s.clusterIP || "–"),
        el("td.small", s.ports.map((p) => el("div.mono", `${p.port}${p.targetPort !== undefined && String(p.targetPort) !== String(p.port) ? `→${p.targetPort}` : ""}/${(p.protocol || "TCP").toLowerCase()}${p.nodePort ? ` (node ${p.nodePort})` : ""}`))),
        el("td.small", s.ports.map((p) => {
          const local = s.type === "LoadBalancer" ? p.port : s.type === "NodePort" ? p.nodePort : 0;
          return el("div", local ? open(local) : el("span.muted", "cluster only"));
        })),
        el("td.actions",
          s.ports.length && s.clusterIP !== "None" ? el("button.ghost.mini", { type: "button", onclick: () => forwardDialog("service", s.namespace, s.name, s.ports.map((p) => p.port)) }, "Forward…") : null,
          el("button.ghost.mini", { type: "button", onclick: () => openYAML("service", s.namespace, s.name) }, "YAML")))), 40))),
    el("p.small.muted", "LoadBalancer and NodePort services reach your Mac by themselves. Forward a ClusterIP service to reach it on localhost."));
  }
  const iCard = el("section.card.info-card.k8s-card2", k8sHead("t5", "net", "Ingresses", ings.length, el("span.small.muted", "HTTP routes by host and path")));
  if (!ings.length) iCard.append(emptyState("net", "No ingresses with this filter", "An Ingress routes web traffic by host name to a Service; it needs an ingress controller such as Traefik."));
  else {
    iCard.append(el("div.table-wrap", el("table.k8s-table",
      el("thead", el("tr", ["Ingress", "Class", "Routes", "Address", ""].map((h) => el("th", h)))),
      el("tbody", ings.map((i) => el("tr",
        el("td", el("strong", i.name), k8s.ns ? null : el("div.muted.small", i.namespace), i.tls ? el("span.pill.ok", "TLS") : null),
        el("td", el(`span.pill.tone-${i.class ? "purple" : "info"}`, i.class || "default")),
        el("td.small", i.rules.map((r) => el("div.mono", `${r.host}${r.path || "/"} → ${r.service || "?"}:${r.port || "?"}`))),
        el("td.small.mono", i.address.join(", ") || "–"),
        el("td.actions", el("button.ghost.mini", { type: "button", onclick: () => openYAML("ingress", i.namespace, i.name) }, "YAML"))))))));
    if (k8s.k && k8s.k.settings && !k8s.k.settings.traefik) iCard.append(el("p.small.muted", "Traefik is off (see Cluster), so nothing serves these unless you installed another ingress controller."));
  }
  return [k8sForwardsCard(() => drawK8sView($(".k8s-body"), true)), sCard, iCard];
}

async function loadForwards() {
  try { k8s.fwd = await api("GET", `${P()}/kubernetes/forwards`); } catch { k8s.fwd = []; }
  return k8s.fwd;
}

// k8sForwardsCard lists the running port forwards; the Port Forwarding page shows it too.
function k8sForwardsCard(after) {
  const card = el("section.card.info-card.k8s-card2", k8sHead("t2", "fwd", "Port forwards", k8s.fwd.length,
    el("span.small.muted", "kubectl port-forward in the VM, on your Mac's localhost")));
  if (!k8s.fwd.length) {
    card.append(emptyState("fwd", "None running", "Use Forward… on a service, or Port forward… in a pod's details, to reach it on localhost."));
    return card;
  }
  card.append(el("table.k8s-table", el("tbody", k8s.fwd.map((f) => el("tr",
    el("td", f.running && f.reachable ? el("a", { href: `http://localhost:${f.local}`, target: "_blank", rel: "noopener noreferrer" }, `localhost:${f.local}`) : el("span.mono", `localhost:${f.local}`)),
    el("td.small", `→ ${f.kind}/${f.name}:${f.remote}`, k8s.ns ? null : el("span.muted", ` · ${f.namespace}`)),
    el("td", el(`span.pill.${!f.running ? "fail" : f.reachable ? "ok" : "warn"}`, !f.running ? "stopped" : f.reachable ? "open" : "starting"),
      !f.running && f.output ? el("div.small.bad-text", f.output.split("\n").slice(-2).join(" ")) : null),
    el("td.actions", el("button.ghost.mini", { type: "button", onclick: async () => {
      try { await api("DELETE", `${P()}/kubernetes/forwards/${encodeURIComponent(f.id)}`); toast(`Stopped localhost:${f.local}`); } catch (e) { toast(e.message, true); }
      await loadForwards();
      if (after) after();
    } }, f.running ? "Stop" : "Remove")))))));
  return card;
}

function suggestLocal(port) {
  if (port >= 1024) return port;
  return port === 80 ? 8080 : port === 443 ? 8443 : 10000 + port;
}

async function forwardDialog(kind, ns, name, ports) {
  ports = [...new Set((ports || []).filter(Boolean))];
  const remote = ports.length
    ? el("select", { "aria-label": "Port in the cluster" }, ports.map((p) => el("option", { value: p }, String(p))))
    : el("input", { type: "number", min: 1, max: 65535, placeholder: "e.g. 80", "aria-label": "Port in the cluster" });
  const local = el("input", { type: "number", min: 1024, max: 65535, value: ports.length ? suggestLocal(ports[0]) : "", "aria-label": "Port on your Mac" });
  remote.addEventListener("change", () => { local.value = suggestLocal(Number(remote.value)); });
  const body = el("div.form",
    el("p.small.muted", `Forward a port of ${kind} ${ns}/${name} to localhost on your Mac. It runs until you stop it or quit Colima Dashboard.`),
    el("label.field", el("span", `Port of the ${kind}`), remote),
    el("label.field", el("span", "Port on your Mac (1024 to 65535)"), local));
  if (!await confirmBox(`Port forward ${name}`, body, "Start forward", false)) return;
  const r = Number(remote.value), l = Number(local.value);
  if (!(r >= 1 && r <= 65535) || !(l >= 1024 && l <= 65535)) { toast("Ports must be 1 to 65535, and 1024 or above on the Mac", true); return; }
  toast(`Starting localhost:${l} → ${name}:${r}…`);
  try {
    const f = await api("POST", `${P()}/kubernetes/forwards`, { kind, namespace: ns, name, remote: r, local: l });
    toast(f.reachable ? `Open: localhost:${l} goes to ${kind}/${name}:${r}` : `localhost:${l} is starting; it shows as open under Services when ready`);
    await loadForwards();
    if (k8s.view === "services" && location.hash === "#kubernetes") refreshK8s();
    if (location.hash === "#ports") render();
  } catch (e) { toast(e.message, true); }
}

// ---------- Events ----------

async function eventsView() {
  const warnOnly = el("input", { type: "checkbox", checked: safeGet("k8sWarnOnly") === "1" });
  const evs = (await api("GET", `${P()}/kubernetes/events${k8s.ns ? `?ns=${encodeURIComponent(k8s.ns)}` : ""}`)).filter((e) => nsShown(e.namespace));
  if (!k8s.ov) await loadOverview().catch(() => {});
  const list = el("div");
  const paint = () => {
    const shown = evs.filter((e) => !warnOnly.checked || e.type === "Warning");
    list.replaceChildren(shown.length ? el("ul.k8s-events", shown.slice(0, 300).map(eventRow))
      : emptyState("clock", warnOnly.checked ? "No warnings" : "No events", "Kubernetes keeps events for an hour.", "ok"));
  };
  warnOnly.addEventListener("change", () => { safeSet("k8sWarnOnly", warnOnly.checked ? "1" : "0"); paint(); });
  paint();
  return [el("section.card.info-card.k8s-card2", k8sHead("t6", "clock", "Events", evs.length, el("label.check.small", warnOnly, "Warnings only")), list)];
}

// ---------- YAML ----------

function yamlEditor(value) {
  return el("textarea.yaml-edit", { spellcheck: "false", autocomplete: "off", autocapitalize: "off", "aria-label": "YAML" }, value || "");
}

// applyYAML sends YAML to kubectl apply, as a dry run or for real, and shows the answer in out.
async function applyYAML(yaml, dryRun, out) {
  out.hidden = false;
  out.className = "log k8s-out";
  out.textContent = dryRun ? "Checking with the API server…" : "Applying…";
  try {
    const r = await api("POST", `${P()}/kubernetes/apply`, { yaml, dryRun });
    out.textContent = (dryRun ? "Dry run passed; nothing was saved.\n" : "") + (r.output || "done");
    return true;
  } catch (e) {
    out.className = "log k8s-out bad";
    out.textContent = e.message;
    return false;
  }
}

async function openYAML(kind, ns, name) {
  const ta = yamlEditor("Loading…");
  ta.disabled = true;
  const out = el("pre.log.k8s-out", { hidden: true });
  let orig = "";
  const dry = el("button.ghost.mini", { type: "button", disabled: true }, "Dry run");
  const apply = el("button.mini", { type: "button", disabled: true }, "Apply…");
  const reload = el("button.ghost.mini", { type: "button" }, "Reload");
  const changed = el("span.small.muted", "");
  const load = async () => {
    ta.disabled = true;
    try {
      const r = await api("GET", `${P()}/kubernetes/yaml?kind=${encodeURIComponent(kind)}&ns=${encodeURIComponent(ns)}&name=${encodeURIComponent(name)}`);
      orig = r.yaml;
      ta.value = r.yaml;
      ta.disabled = false;
      dry.disabled = apply.disabled = false;
      changed.textContent = "";
    } catch (e) { ta.value = ""; out.hidden = false; out.className = "log k8s-out bad"; out.textContent = e.message; }
  };
  ta.addEventListener("input", () => { changed.textContent = ta.value === orig ? "" : "edited"; });
  dry.addEventListener("click", () => applyYAML(ta.value, true, out));
  apply.addEventListener("click", async () => {
    if (ta.value === orig) { toast("Nothing changed"); return; }
    const pods = ["deployment", "statefulset", "daemonset"].includes(kind) ? " If the pod template changed, its pods roll." : kind === "pod" ? " Most fields of a running pod cannot change; the API server says which." : "";
    if (!await confirmBox(`Apply changes to ${kind} ${name}?`, `kubectl apply sends this YAML to the cluster.${pods}`, "Apply", false)) return;
    if (await applyYAML(ta.value, false, out)) { toast(`Applied ${kind} ${name}`); await load(); refreshK8s(); }
  });
  reload.addEventListener("click", load);
  openK8sDrawer(`YAML: ${kind} ${ns}/${name}`,
    el("div.toolbar", dry, apply, reload, el("button.ghost.mini", { type: "button", onclick: () => download(`${name}.${kind}.yaml`, ta.value) }, "Download"), changed),
    el("p.small.muted", "Edit and apply in place. managedFields are left out; status is ignored when applying."),
    ta, out);
  await load();
}

const SAMPLE_YAML = `# Paste one or more manifests, separated by ---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: hello
  namespace: default
spec:
  replicas: 1
  selector:
    matchLabels: { app: hello }
  template:
    metadata:
      labels: { app: hello }
    spec:
      containers:
        - name: web
          image: nginx:1.27-alpine
          ports: [{ containerPort: 80 }]
          resources:
            requests: { cpu: 100m, memory: 64Mi }
            limits: { cpu: 250m, memory: 128Mi }
          readinessProbe: { httpGet: { path: /, port: 80 } }
`;

async function applyView() {
  const ta = yamlEditor(k8s.draft);
  ta.placeholder = SAMPLE_YAML;
  ta.addEventListener("input", () => { k8s.draft = ta.value; });
  const out = el("pre.log.k8s-out", { hidden: true });
  const dry = el("button.ghost", { type: "button", onclick: () => ta.value.trim() ? applyYAML(ta.value, true, out) : toast("Paste a manifest first", true) }, "Dry run");
  const apply = el("button", { type: "button" }, "Apply…");
  apply.addEventListener("click", async () => {
    if (!ta.value.trim()) { toast("Paste a manifest first", true); return; }
    const kinds = [...ta.value.matchAll(/^kind:\s*(\S+)/gm)].map((m) => m[1]);
    if (!await confirmBox("Apply this YAML?", `kubectl apply creates or updates ${kinds.length ? kinds.join(", ") : "what it describes"} in the cluster.`, "Apply", false)) return;
    if (await applyYAML(ta.value, false, out)) toast("Applied");
  });
  return [el("section.card.info-card.k8s-card2.k8s-apply",
    k8sHead("t7", "code", "Apply YAML", null,
      el("button.ghost.mini", { type: "button", onclick: () => { ta.value = SAMPLE_YAML; k8s.draft = ta.value; } }, "Insert example")),
    el("div.toolbar.k8s-apply-bar", dry, apply),
    el("p.small.muted", "Runs kubectl apply inside the VM. Dry run asks the API server to check everything, including that the fields exist, without saving anything. Objects without a namespace go to default."),
    ta, out)];
}

// ---------- Cluster: settings, kubectl context, nodes ----------

async function clusterView() {
  const k = k8s.k;
  const ctx = el("section.card.info-card.k8s-card2", k8sHead("t6", "term", "kubectl on your Mac", null));
  if (k.currentContextError) {
    ctx.append(el("p", k.currentContextError + ". Colima wrote the context ", el("code", k.context), " to ~/.kube/config. This page does not need it: it runs kubectl inside the VM."), fixRow({ id: "kubectl", title: "kubectl", fix: "brew install kubernetes-cli", action: "install" }));
  } else if (k.currentContext !== k.context) {
    ctx.append(el("p", "kubectl points at ", el("code", k.currentContext), ", not this profile."), fixRow({ id: "kubectx", title: "kubectl context", fix: `kubectl config use-context ${k.context}`, action: "terminal" }));
  } else {
    ctx.append(el("p", el("span.pill.ok", "ok"), " kubectl uses ", el("code", k.context), "."));
  }
  return [k8sSettingsCard(k), ctx,
    el("details.card", el("summary", el("strong", "Nodes and version")), el("pre.log", ((k.nodes || k.nodesError || "") + "\n" + (k.version || "")).trim()))];
}

// Settings: on or off, version, Traefik, reset. Each change restarts the VM, so each asks first.
function k8sSettingsCard(k) {
  const set = k.settings || {};
  const restartNote = `Colima restarts profile ${state.profile}; running containers stop for a minute.`;
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
  return el("section.card.k8s-card",
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
}

tabs.kubernetes = renderKubernetes;
