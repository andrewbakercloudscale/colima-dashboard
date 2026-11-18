// Fix recommendations: the "Fix" buttons next to warnings (out of memory, high CPU, close to a
// limit), the drawer that explains each one with current versus recommended values and applies
// them in one click, and the Recommendations card on General and Monitor.
// Loaded before app.js; it only uses app.js and containers.js helpers (el, api, P, state,
// toast, openDrawer, closeDrawer, confirmBox, runJob, copyBtn, render, showLimits) when called.
"use strict";

// One answer per profile, shared by every page. The server caches for 30 seconds too and
// works it out only when asked: nothing here polls on its own.
const advice = { profile: null, data: null, at: 0, pending: null };
const ADVICE_TTL = 30e3;

function adviceFresh() { return advice.profile === state.profile && advice.data && Date.now() - advice.at < ADVICE_TTL; }

// loadAdvice returns the profile's recommendations, from memory when under 30 seconds old.
// force asks the server for a new answer too (after a change was applied).
async function loadAdvice(force) {
  if (!force && adviceFresh()) return advice.data;
  if (advice.pending && advice.pending.profile === state.profile && !force) return advice.pending.p;
  const profile = state.profile;
  const p = api("GET", `${P()}/advice${force ? "?fresh=1" : ""}`).then((d) => {
    if (profile === state.profile) Object.assign(advice, { profile, data: d, at: Date.now() });
    fillFixSlots();
    return d;
  }).finally(() => { if (advice.pending?.p === p) advice.pending = null; });
  advice.pending = { profile, p };
  return p;
}

function adviceIssues() { return advice.profile === state.profile && advice.data ? advice.data.issues : []; }
function findAdvice(match) { return adviceIssues().find(match) || null; }

// Matchers for the places a warning shows.
const adviceFor = {
  container: (id, kinds) => (is) => is.target.containerId === id && (!kinds || kinds.includes(is.kind)),
  job: (job) => (is) => is.target.job === job && (is.kind === "job-oom" || is.kind === "oom"),
};

// fixSlot is a placeholder that turns into a Fix button when the advice has a matching issue,
// now or when it next arrives. render(issue) may return something else to show (a pill, say).
function fixSlot(match, opts = {}) {
  const span = el("span.fix-slot");
  span._match = match;
  span._opts = opts;
  fillSlot(span);
  return span;
}
function fillSlot(span) {
  const is = findAdvice(span._match);
  if (!is) { span.replaceChildren(); return; }
  span.replaceChildren(span._opts.render ? span._opts.render(is) : fixButton(is, span._opts));
}
function fillFixSlots() { for (const s of document.querySelectorAll(".fix-slot")) fillSlot(s); }

// fixButton opens the drawer for one issue.
function fixButton(is, opts = {}) {
  return el(`button.mini.fix-btn${is.severity === "bad" ? ".bad" : ""}`, {
    type: "button", title: `${is.title}: see what to change`,
    onclick: (e) => { e.stopPropagation(); showFix(is, opts); },
  }, opts.label || "Fix");
}

const plural = (n, one, many) => (n === 1 ? one : many || one + "s");

// ---------- applying ----------

async function applyLimits(is, body, start) {
  const t = is.target;
  await api("POST", `${P()}/containers/${encodeURIComponent(t.containerId)}/limits`, body);
  if (start) await api("POST", `${P()}/containers/${encodeURIComponent(t.containerId)}/start`);
}

function limitsWords(body) {
  return [body.memory && `memory limit ${body.memory}`, body.cpus && `${body.cpus} ${+body.cpus === 1 ? "CPU" : "CPUs"}`].filter(Boolean).join(" and ");
}

async function afterApply(msg) {
  toast(msg);
  closeDrawer();
  try { await loadAdvice(true); } catch { /* the page shows the error on its next load */ }
  if (typeof render === "function") render();
}

async function confirmLimits(is, start) {
  const t = is.target;
  const ok = await confirmBox(start ? `Change and start ${t.name}?` : `Change ${t.name}?`, el("div",
    el("p", `Sets ${limitsWords(is.apply.body)} on ${t.name}. ${t.state === "running" ? "It applies at once, without a restart." : start ? "Then starts it." : "It takes effect the next time it starts."}`),
    t.service ? el("p.muted.small", `It belongs to Compose service "${t.service}": the next docker compose up recreates it from the compose file, so copy the compose snippet into it too.`) : null),
  start ? "Apply and start" : "Apply");
  if (!ok) return;
  try {
    await applyLimits(is, is.apply.body, start);
    afterApply(`${t.name}: ${limitsWords(is.apply.body)}${start ? ", started" : ""}`);
  } catch (e) { toast(e.message, true); }
}

// VM changes restart the VM, so they are never applied from here: Preferences opens with the
// recommended values filled in, and Apply there (with its own confirmation) does it.
function prefsHref(apply) {
  const q = new URLSearchParams();
  if (apply.body.cpus) q.set("cpus", apply.body.cpus);
  if (apply.body.memoryGiB) q.set("memoryGiB", apply.body.memoryGiB);
  return `#preferences?${q}`;
}
function openPrefs(apply) {
  closeDrawer();
  location.hash = prefsHref(apply);
}
const prefsLabel = (apply) => (apply.body.cpus ? `Set ${apply.body.cpus} CPUs in Preferences` : `Set ${apply.body.memoryGiB} GiB in Preferences`);

// limitConsumer sets a limit on one of the containers named by a VM-wide issue.
async function limitConsumer(c) {
  const body = c.field === "cpus" ? { cpus: c.suggest } : { memory: c.suggest };
  const ok = await confirmBox(`Limit ${c.name}?`, el("p", `Sets ${limitsWords(body)} on ${c.name}, at once and without a restart. It now uses ${c.use}.`), "Apply");
  if (!ok) return;
  try {
    await api("POST", `${P()}/containers/${encodeURIComponent(c.containerId)}/limits`, body);
    afterApply(`${c.name}: ${limitsWords(body)}`);
  } catch (e) { toast(e.message, true); }
}

// ---------- the drawer ----------

function compareTable(rows) {
  return el("table.fix-compare",
    el("thead", el("tr", el("th", ""), el("th", "Now"), el("th", "Recommended"))),
    el("tbody", rows.map((r) => {
      const same = r.current === r.recommended;
      return el(`tr${same ? ".same" : ".changed"}`, el("th", { scope: "row" }, r.label), el("td.mono", r.current),
        el("td.mono", same ? el("span", r.recommended) : el("strong", r.recommended)));
    })));
}

function snippetBlock(s) {
  return el("div.fix-snippet",
    el("div.fix-snippet-head", el("span.small.muted", s.label), copyBtn(s.text)),
    el("pre.log.fix-pre", s.text));
}

function showFix(is, opts = {}) {
  const t = is.target;
  const actions = el("div.fix-actions");
  if (is.apply.type === "limits") {
    actions.append(el("button", { type: "button", onclick: () => confirmLimits(is, false) }, "Apply recommendations"));
    if (t.state && t.state !== "running" && is.kind === "oom") {
      actions.append(el("button.ghost", { type: "button", onclick: () => confirmLimits(is, true) }, "Apply and start"));
    }
  } else if (is.apply.type === "vm") {
    actions.append(el("button", { type: "button", onclick: () => openPrefs(is.apply) }, `${prefsLabel(is.apply)}…`),
      el("span.small.muted", "Applying it there restarts the VM"));
  }
  if (is.vmApply) {
    actions.append(el("button.ghost", { type: "button", onclick: () => openPrefs(is.vmApply) }, `Then: ${prefsLabel(is.vmApply).replace(/^Set/, "set")}…`));
  }
  const links = (is.links || []).map((l) => el("a.btn.ghost.mini", { href: l.href, onclick: () => closeDrawer() }, l.label));
  if (links.length) actions.append(...links);

  const cons = is.consumers?.length ? el("div.fix-section",
    el("h3", is.kind === "vm-cpu" ? "Busiest containers" : "Biggest containers"),
    el("ul.fix-cons", is.consumers.map((c) => el("li",
      el("div", el("strong", c.name), el("span.muted.small", ` ${c.use}, ${c.share}% of the VM${c.limit ? `, limit ${c.limit}` : ", no limit"}`)),
      el("button.ghost.mini", { type: "button", onclick: () => limitConsumer(c) }, `Limit to ${c.suggest}${c.field === "cpus" ? (+c.suggest === 1 ? " CPU" : " CPUs") : ""}`))))) : null;

  // The manual route: the limits editor with the recommended values filled in, or Preferences.
  let adjust = null;
  if (t.containerId && (is.apply.type === "limits" || is.recommended?.memory || is.recommended?.cpus)) {
    adjust = el("details.fix-adjust",
      el("summary", "Adjust values yourself"),
      el("p.small.muted", "Opens the limits editor with the recommended values filled in. Change them, then Apply there."),
      el("button.ghost.mini", { type: "button", onclick: () => showLimits({ Id: t.containerId, State: t.state }, t.name, { memory: is.recommended?.memory, cpus: is.recommended?.cpus }) }, "Open the limits editor"));
  }

  openDrawer(`Fix: ${is.title}`, ...[
    el(`div.card.fix-main.${is.severity === "bad" ? "fail" : "warn"}`,
      el("div.fix-title", el(`span.sev.${is.severity === "bad" ? "fail" : "warn"}`, is.severity === "bad" ? "Problem" : "Warning"),
        t.name ? el("span.muted.small", t.containerId ? `container ${t.name}` : t.name) : el("span.muted.small", `profile ${t.profile}`)),
      el("p.fix-why", is.why),
      is.rows?.length ? compareTable(is.rows) : null,
      actions.childElementCount ? actions : el("p.small.muted", "There is nothing to apply from here: copy the change below into where you start it.")),
    cons,
    is.snippets?.length ? el("div.card.fix-section", el("h3", is.apply.type === "none" ? "Make the change" : "To keep it next time"),
      is.snippets.map(snippetBlock)) : null,
    adjust].filter(Boolean));
}

// ---------- Recommendations card ----------

const autoApplicable = (is) => is.apply.type === "limits";

async function applyAll(list) {
  const ok = await confirmBox(`Apply ${list.length} ${plural(list.length, "change")}?`, el("div",
    el("ul.fix-confirm", list.map((is) => el("li", el("strong", is.target.name), ` ${limitsWords(is.apply.body)}`))),
    el("p.muted.small", "Each applies at once to its container, without a restart. VM changes are not included: they restart the VM, so apply them on their own.")),
  "Apply all");
  if (!ok) return;
  let done = 0;
  const errs = [];
  for (const is of list) {
    try { await applyLimits(is, is.apply.body, false); done++; } catch (e) { errs.push(`${is.target.name}: ${e.message}`); }
  }
  if (errs.length) toast(`${done} applied; ${errs.join("; ")}`, true);
  afterApply(`${done} ${plural(done, "change")} applied`);
}

function adviceItem(is) {
  return el(`li.fix-item.${is.severity === "bad" ? "fail" : "warn"}`,
    el("div.fix-title", el("strong", is.title), el(`span.sev.${is.severity === "bad" ? "fail" : "warn"}`, is.severity === "bad" ? "Problem" : "Warning")),
    el("p.fix-detail", is.why),
    el("div.fix-row", fixButton(is, { label: is.apply.type === "none" ? "See the fix" : "Fix" }),
      is.rows?.length ? el("span.small.muted", is.rows.filter((r) => r.current !== r.recommended).map((r) => `${r.label}: ${r.current} → ${r.recommended}`).join(" · ")) : null));
}

// adviceRow is one recommendation as a row of General's "things to fix" card: title and badge,
// one line with the numbers, then the actions. Container limits apply after a confirmation;
// VM changes open Preferences with the values filled in, never more.
function adviceRow(is) {
  const bad = is.severity === "bad";
  const btns = [];
  if (is.apply.type === "limits") btns.push(el("button.mini", { type: "button", onclick: () => confirmLimits(is, false) }, "Apply recommendation"));
  else if (is.apply.type === "vm") btns.push(el("button.mini", { type: "button", onclick: () => openPrefs(is.apply) }, `${prefsLabel(is.apply)}…`));
  btns.push(el("button.ghost.mini", { type: "button", onclick: () => showFix(is) }, "Fix…"));
  return el(`li.fix-item.${bad ? "fail" : "warn"}`,
    el("div.fix-title", el("strong", is.title), el(`span.sev.${bad ? "fail" : "warn"}`, bad ? "Problem" : "Warning")),
    el("p.fix-detail", is.summary || is.why),
    el("div.copy", btns));
}

// adviceCard is the Recommendations card, or nothing when there is nothing to recommend.
function adviceCard(d) {
  const issues = d?.issues || [];
  if (!issues.length) return null;
  const vm = issues.filter((is) => !is.target.containerId && is.kind.startsWith("vm-"));
  const rest = issues.filter((is) => !vm.includes(is));
  const auto = rest.filter(autoApplicable);
  return el("section.card.fix-card.advice-card",
    el("div.fix-head",
      el("div", el("h2", "Recommendations"),
        el("p.small.muted.advice-src", d.source === "monitor" ? "From the Monitor's live samples." : d.source === "snapshot" ? "From a one-off reading. Open the Monitor for CPU advice over time." : "From container settings and the runs history.")),
      auto.length ? el("button.mini", { type: "button", onclick: () => applyAll(auto), title: "Applies every container change below at once; VM changes are left out" },
        auto.length === 1 ? "Apply recommendation" : `Apply all recommendations (${auto.length})`) : null),
    rest.length ? el("ul.fix-list", rest.map(adviceItem)) : null,
    vm.length ? el("div.advice-vm",
      el("h3.advice-sub", "VM changes", el("span.muted.small", " restart the VM, so they are set in Preferences")),
      el("ul.fix-list", vm.map(adviceItem))) : null);
}

// adviceSlot is a box that holds the Recommendations card when there is something to say.
// Its tick() asks again once the answer is 30 seconds old (the Monitor calls it every tick).
// lazy shows a recent answer at once but leaves asking for a new one to tick().
function adviceSlot(lazy) {
  const box = el("div.advice-slot");
  const paint = (d) => box.replaceChildren(...[adviceCard(d)].filter(Boolean));
  const load = () => loadAdvice().then(paint).catch(() => { /* recommendations are extra: say nothing */ });
  box.tick = () => { if (!adviceFresh() && !advice.pending) load(); else if (!box.childElementCount && adviceFresh()) paint(advice.data); };
  if (!lazy || adviceFresh()) load();
  return box;
}
