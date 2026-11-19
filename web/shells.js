"use strict";
// Shells: one place to open a terminal in the Colima VM, any running container or a Kubernetes
// pod. Each opens in the side panel (showShell in app.js); closing the panel ends the shell.

function shellPrefs() {
  let p = {};
  try { p = JSON.parse(safeGet("shellprefs") || "{}") || {}; } catch { p = {}; }
  return { shell: ["bash", "sh"].includes(p.shell) ? p.shell : "", user: typeof p.user === "string" ? p.user : "" };
}

function shellRecent() {
  try { const r = JSON.parse(safeGet(`shellrecent:${state.profile}`) || "[]"); return Array.isArray(r) ? r.slice(0, 6) : []; } catch { return []; }
}
function rememberShell(item) {
  const key = (x) => `${x.kind}:${x.name || ""}`;
  const list = [item, ...shellRecent().filter((x) => key(x) !== key(item))].slice(0, 6);
  safeSet(`shellrecent:${state.profile}`, JSON.stringify(list));
}

function openVMShell() {
  const { shell } = shellPrefs();
  rememberShell({ kind: "vm" });
  showShell("vm", `Colima VM (${state.profile})`, {
    url: `${P()}/vm/shell`, body: { shell },
    note: "Root inside the Colima VM, where Docker, containerd and k3s run. Closing this panel ends the shell.",
  });
}

async function openVMTerminal() {
  try {
    const r = await api("POST", `${P()}/vm/terminal`);
    toast(`Opened Terminal: ${r.command}`);
  } catch (e) { toast(e.message, true); }
}

function openContainerShell(c) {
  const name = (c.Names?.[0] || c.Id.slice(0, 12)).replace(/^\//, "");
  const { shell, user } = shellPrefs();
  rememberShell({ kind: "ctr", name });
  const as = user ? ` as ${user}` : "";
  showShell(c.Id, name, {
    body: { name, shell, user },
    note: `${shell || "bash, or sh when the image has no bash"}${as}. Closing this panel ends the shell.`,
  });
}

async function renderShells(main) {
  const prefs = shellPrefs();
  const shellSel = el("select", { "aria-label": "Shell" },
    [["", "bash if there, else sh"], ["bash", "bash"], ["sh", "sh"]].map(([v, t]) => el("option", { value: v, selected: v === prefs.shell }, t)));
  const userBox = el("input", { type: "text", placeholder: "image's own user", value: prefs.user, maxlength: 32, spellcheck: false, "aria-label": "User" });
  const save = () => safeSet("shellprefs", JSON.stringify({ shell: shellSel.value, user: userBox.value.trim() }));
  shellSel.addEventListener("change", save);
  userBox.addEventListener("input", save);
  const head = el("div.head",
    el("div", el("h1", "Shells"),
      el("p.muted", "Open a terminal in the Colima VM, a running container or a Kubernetes pod, right here in the page.")),
    el("div.toolbar.sh-prefs",
      el("label.sh-pref", el("span", "Shell"), shellSel),
      el("label.sh-pref", el("span", "User"), userBox)));
  if (!isRunning()) { main.replaceChildren(head, notRunningCard()); return; }

  const vm = el("div.card.info-card.sh-vm", cardHead("t3", "term", "Colima VM", `profile ${state.profile}`),
    el("div.sh-vm-body",
      el("div.sh-vm-text",
        el("p", "A root shell inside the virtual machine itself: the same machine ", el("code", "colima ssh"), " reaches. Docker, containerd and k3s run here, so this is where to look at disks, networking and the engine's own logs."),
        el("p.muted.small", "It runs through a small helper container that is removed when you close the shell. The user setting does not apply here.")),
      el("div.sh-vm-actions",
        el("button.solid", { type: "button", "data-act": "go", "data-act-fixed": "", onclick: openVMShell }, "Open VM shell"),
        el("button.ghost", { type: "button", onclick: openVMTerminal }, "Open in Terminal"))));

  const recentBox = el("div.sh-recent");
  const filter = el("input", { type: "search", placeholder: "Filter by name or image", "aria-label": "Filter containers", value: safeGet("shfilter") ?? "" });
  const ctrList = el("div.sh-list", el("p.muted.small", "Loading…"));
  const ctrCard = el("div.card.info-card", cardHead("t1", "box", "Containers", "running ones only"), el("div.sh-filter", filter), ctrList);
  const k8sOn = currentProfile()?.kubernetes || /k3s/.test(currentProfile()?.runtime || "");
  const podList = el("div.sh-list", el("p.muted.small", "Loading…"));
  const podCard = k8sOn ? el("div.card.info-card", cardHead("t2", "layers", "Kubernetes pods", "running ones only"), podList) : null;
  const sum = sumBox();
  main.replaceChildren(head, sum, recentBox, vm, ctrCard, podCard || "");
  const tally = { ctrs: null, pods: null, projects: 0 };
  const summarise = () => pageSummary(sum, [
    tally.ctrs === null ? null : [tally.ctrs, "running containers", "Each one can open a shell here"],
    tally.ctrs === null ? null : [tally.projects, "Compose projects"],
    podCard && tally.pods !== null ? [tally.pods, "running pods"] : null,
    [shellRecent().length, "recent shells", "Shells you opened lately, one click to reopen"],
  ]);

  let running = [];
  const drawRecent = () => {
    const items = shellRecent().filter((r) => r.kind === "vm" || running.some((c) => c.Names?.some((n) => n.replace(/^\//, "") === r.name)));
    recentBox.hidden = !items.length;
    recentBox.replaceChildren(el("span.sh-recent-label", "Recent"), ...items.map((r) => {
      if (r.kind === "vm") return el("button.mini.sh-chip.vm", { type: "button", onclick: openVMShell }, "Colima VM");
      const c = running.find((x) => x.Names?.some((n) => n.replace(/^\//, "") === r.name));
      return el("button.mini.sh-chip", { type: "button", onclick: () => openContainerShell(c) }, r.name);
    }));
  };
  const drawCtrs = () => {
    const q = filter.value.trim().toLowerCase();
    safeSet("shfilter", filter.value);
    const rows = running.filter((c) => !q || `${c.Names.join(" ")} ${c.Image}`.toLowerCase().includes(q));
    if (!running.length) { ctrList.replaceChildren(el("p.empty", "No containers are running. Start one on the Containers page, or try: docker run -d nginx")); return; }
    if (!rows.length) { ctrList.replaceChildren(el("p.empty", "Nothing matches the filter.")); return; }
    ctrList.replaceChildren(...rows.map((c, i) => {
      const name = (c.Names?.[0] || c.Id.slice(0, 12)).replace(/^\//, "");
      const proj = c.Labels?.["com.docker.compose.project"];
      const row = el("div.sh-row",
        el("span.sh-dot"),
        el("div.sh-main", el("strong.mono", name),
          el("span.sh-sub", c.Image, proj ? el("span.pill", proj) : null, c.Status ? el("span.muted", ` · ${c.Status}`) : null)),
        el("button.mini", { type: "button", onclick: () => openContainerShell(c) }, "Open shell"));
      row.style.setProperty("--i", Math.min(i, 30));
      return row;
    }));
  };
  filter.addEventListener("input", drawCtrs);
  try {
    const all = await api("GET", `${P()}/containers`);
    running = all.filter((c) => c.State === "running" && !c.Labels?.["io.github.colima-dashboard.helper"])
      .sort((a, b) => a.Names[0].localeCompare(b.Names[0]));
    drawCtrs();
    tally.ctrs = running.length;
    tally.projects = new Set(running.map((c) => c.Labels?.["com.docker.compose.project"]).filter(Boolean)).size;
  } catch (e) { ctrList.replaceChildren(el("div.notice.bad", e.message)); }
  drawRecent();
  summarise();

  if (!podCard) return;
  try {
    const ov = await api("GET", `${P()}/kubernetes/overview`);
    const pods = (ov.pods || []).filter((p) => p.phase === "Running").sort((a, b) => `${a.namespace}/${a.name}`.localeCompare(`${b.namespace}/${b.name}`));
    tally.pods = pods.length; summarise();
    if (!pods.length) { podList.replaceChildren(el("p.empty", "No pods are running.")); return; }
    podList.replaceChildren(...pods.map((p) => el("div.sh-row.pod",
      el("span.sh-dot"),
      el("div.sh-main", el("strong.mono", p.name),
        el("span.sh-sub", el("span.pill", p.namespace), ` ${p.containers.map((c) => c.name).join(", ")}`)),
      el("button.mini", { type: "button", onclick: () => podShell(p) }, "Open shell"))));
  } catch (e) { podList.replaceChildren(el("div.notice.bad", e.message)); }
}

tabs.shells = renderShells;
