"use strict";
// Compose from a folder (Containers page) and volume backups (Volumes page).

// ---------- Compose from a folder ----------

function composeRecent() {
  try { const r = JSON.parse(safeGet("composerecent") || "[]"); return Array.isArray(r) ? r.filter((x) => typeof x === "string").slice(0, 8) : []; } catch { return []; }
}
function rememberCompose(dir) {
  safeSet("composerecent", JSON.stringify([dir, ...composeRecent().filter((d) => d !== dir)].slice(0, 8)));
}
function tildePath(p) {
  const home = state.overview?.home || "";
  return home && p.startsWith(home + "/") ? "~" + p.slice(home.length) : p;
}

// The picker: type a folder, pick a recent one, or find the projects on this Mac.
function showComposePicker() {
  const input = el("input.grow", { type: "text", placeholder: "~/code/my-app", "aria-label": "Folder with a compose.yaml", value: tildePath(composeRecent()[0] || "") });
  const go = () => { const d = input.value.trim(); if (d) showCompose(d); };
  input.addEventListener("keydown", (e) => { if (e.key === "Enter") go(); });
  const found = el("div.cmp-found");
  const recent = composeRecent();
  const pickRow = (dir, note) => el("button.cmp-pick.noact", { type: "button", onclick: () => showCompose(dir) },
    chip("folder"), el("span.cmp-pick-dir.mono", tildePath(dir)), note ? el("span.muted.small", note) : null);
  const find = async (btn) => {
    btn.disabled = true;
    found.replaceChildren(el("p.muted", "Looking in your home folder and the usual code folders…"));
    try {
      const d = await api("GET", "/api/compose/find");
      found.replaceChildren(...[d.projects.length
        ? el("div.cmp-list", d.projects.map((p) => pickRow(p.dir, `${p.file} · changed ${ago(p.modified)}`)))
        : el("p.empty", "No compose.yaml or docker-compose.yml found. Type the folder above instead."),
      d.partial ? el("p.small.muted", "Stopped after 4 seconds; type the folder if yours is not listed.") : null].filter(Boolean));
    } catch (e) { found.replaceChildren(el("div.notice.bad", e.message)); }
    btn.disabled = false;
  };
  const findBtn = el("button.ghost", { type: "button", onclick: (e) => find(e.currentTarget) }, "Find projects on this Mac");
  openDrawer("Compose: from a folder",
    el("div.card.cmp-card", cardHead("t3", "folder", "Run a Compose project", "any folder with a compose.yaml"),
      el("div.cmp-row", input, el("button", { type: "button", onclick: go }, "Open")),
      recent.length ? el("div", el("h3.small-h", "Recent"), el("div.cmp-list", recent.map((d) => pickRow(d)))) : null,
      el("div.cmp-row", findBtn), found));
  input.focus();
}

const COMPOSE_STATE = { running: ["ok", "running"], partial: ["warn", "partly running"], exited: ["x", "stopped"], created: ["x", "created"], paused: ["warn", "paused"], restarting: ["warn", "restarting"], "": ["x", "not started"] };

async function showCompose(dir) {
  const body = el("div", el("p.muted", "Reading the Compose file…"));
  openDrawer(`Compose: ${dir.split("/").filter(Boolean).pop() || dir}`, body);
  let d;
  try { d = await api("GET", `${P()}/compose?dir=${encodeURIComponent(dir)}`); } catch (e) {
    body.replaceChildren(el("div.notice.bad", e.message), el("div.cmp-row", el("button.ghost", { type: "button", onclick: showComposePicker }, "Back to folders")));
    return;
  }
  rememberCompose(d.dir);
  const refresh = () => { if (!$("#drawer").hidden) showCompose(d.dir); };
  const act = async (action, service) => {
    if (action === "down" && !await confirmBox(`Take ${d.project} down?`,
      el("p", "Stops and removes this project's containers and networks. Named volumes and images stay."), "Down", true)) return;
    runJob("POST", `${P()}/compose/${action}`, { dir: d.dir, service: service || "" }, refresh);
  };
  const svcs = d.services;
  const running = svcs.filter((s) => s.state === "running").length;
  const anyUp = svcs.some((s) => s.containers > 0);
  const rows = svcs.map((s) => {
    const [tone, word] = COMPOSE_STATE[s.state] || ["x", s.state];
    return el("tr",
      el("td", el("strong", s.name), s.health ? el("span.muted.small", ` · ${s.health}`) : null),
      el("td.mono.small", s.image || "", s.build ? el("span.pill.cmp-build", "built here") : null),
      el("td.mono.small", s.ports.length ? s.ports.join(", ") : "–"),
      el("td", el(`span.pill.${tone}`, word)),
      el("td.actions",
        s.state === "running"
          ? [el("button.ghost.mini", { type: "button", onclick: () => act("restart", s.name) }, "Restart"),
            el("button.ghost.mini", { type: "button", onclick: () => act("stop", s.name) }, "Stop")]
          : el("button.ghost.mini", { type: "button", onclick: () => act("up", s.name) }, "Start")));
  });
  body.replaceChildren(
    el("div.card.cmp-card", cardHead("t3", "layers", d.project || "Compose project", d.file),
      el("p.mono.small.cmp-dir", tildePath(d.dir)),
      el("div.stat-row.page-sum",
        stat(`${running} of ${svcs.length}`, "services running", "", running === svcs.length && svcs.length ? "ok" : running ? "warn" : ""),
        stat(svcs.filter((s) => s.build).length, "built from this folder"),
        stat(svcs.reduce((a, s) => a + s.ports.filter((p) => p.includes("→")).length, 0), "published ports")),
      d.psError ? el("div.notice.warn", d.psError) : null,
      el("div.cmp-row.cmp-acts",
        el("button", { type: "button", onclick: () => act("up") }, anyUp ? "Start all" : "Start project"),
        svcs.some((s) => s.build) ? el("button.ghost", { type: "button", onclick: () => act("rebuild") }, "Rebuild and start") : null,
        el("button.ghost", { type: "button", onclick: () => act("pull") }, "Pull images"),
        anyUp ? el("button.ghost", { type: "button", onclick: () => act("restart") }, "Restart all") : null,
        anyUp ? el("button.ghost", { type: "button", onclick: () => act("stop") }, "Stop all") : null,
        anyUp ? el("button.ghost.danger", { type: "button", onclick: () => act("down") }, "Down") : null,
        anyUp && d.project ? el("button.ghost", { type: "button", onclick: () => showProjectLogs(d.project) }, "Logs") : null),
      el("div.table-wrap", el("table",
        el("thead", el("tr", ["Service", "Image", "Ports", "State", ""].map((h) => el("th", h)))),
        el("tbody", rows)))),
    el("div.cmp-row", el("button.ghost", { type: "button", onclick: refresh }, "Refresh"), el("button.ghost", { type: "button", onclick: showComposePicker }, "Back to folders")));
}

// ---------- volume backups ----------

async function backupVolume(v) {
  runJob("POST", `${P()}/volumes/${encodeURIComponent(v.Name)}/backup`, null, () => { if (state.tab === "volumes") render(); });
}

async function backupsCard() {
  const card = el("div.card.bk-card", cardHead("t4", "disk", "Volume backups", "plain .tar.gz files on your Mac; they survive deleting the VM"), el("p.muted", "Loading…"));
  let d;
  try { d = await api("GET", "/api/volume-backups"); } catch (e) { card.replaceChildren(card.firstChild, el("div.notice.bad", e.message)); return card; }
  const reveal = (file) => api("POST", "/api/volume-backups/reveal", { file: file || "" }).catch((e) => toast(e.message, true));
  const total = d.backups.reduce((a, b) => a + b.size, 0);
  const tools = el("div.cmp-row", el("span.muted.small.mono.grow", tildePath(d.dir)), el("button.ghost", { type: "button", onclick: () => reveal("") }, "Show in Finder"));
  if (!d.backups.length) {
    card.replaceChildren(card.firstChild, tools, el("p.empty", "No backups yet. Press Back up on a volume above; the backup lands in this folder."));
    return card;
  }
  const rows = d.backups.map((b) => el("tr",
    el("td", el("strong.mono", b.volume)),
    el("td.small", ago(b.time)),
    el("td", bytes(b.size)),
    el("td.actions.wrap",
      el("button.ghost.mini", { type: "button", onclick: () => restoreBackup(b) }, "Restore"),
      el("button.ghost.mini", { type: "button", onclick: () => reveal(b.file) }, "Show in Finder"),
      el("button.ghost.mini.danger", { type: "button", onclick: () => deleteBackup(b) }, "Delete"))));
  card.replaceChildren(card.firstChild, tools,
    el("p.small.muted", `${d.backups.length} backup${d.backups.length === 1 ? "" : "s"}, ${bytes(total)} in all.`),
    el("div.table-wrap", el("table", el("thead", el("tr", ["Volume", "Taken", "Size", ""].map((h) => el("th", h)))), el("tbody", rows))));
  return card;
}

async function restoreBackup(b) {
  const name = el("input", { type: "text", value: b.volume, "aria-label": "Volume to restore into", autocomplete: "off" });
  const ok = await confirmBox("Restore volume?", el("div",
    el("p", "Replaces everything in the volume with ", el("code", b.file), ". A volume that does not exist yet is created, so a new name gives you a copy."),
    el("label.field", el("span", "Restore into volume"), name),
    el("p.small.muted", "Containers using the volume must be stopped first.")), "Restore", true);
  if (!ok) return;
  runJob("POST", `${P()}/volume-restore`, { file: b.file, volume: name.value.trim() }, () => { if (state.tab === "volumes") render(); });
}

async function deleteBackup(b) {
  if (!await confirmBox("Delete backup?", el("p", el("code", b.file), ` (${bytes(b.size)}) is deleted from your Mac.`), "Delete", true)) return;
  try { await api("DELETE", `/api/volume-backups/${encodeURIComponent(b.file)}`); toast("Backup deleted"); render(); } catch (e) { toast(e.message, true); }
}
