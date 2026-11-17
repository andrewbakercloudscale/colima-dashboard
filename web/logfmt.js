// Log formatting for the dashboard: colour-codes log lines without ever using innerHTML.
// Understands ANSI colour codes, logfmt (time=... level=... msg=..., what colima and lima
// print), JSON lines ({"level":..,"msg":..}), Docker's timestamp prefix, level words
// (ERROR, WARN, INFO, DEBUG) and HTTP status codes in access logs.
"use strict";

(function () {
  const span = (cls, text) => {
    const s = document.createElement("span");
    if (cls) s.className = cls;
    s.textContent = text;
    return s;
  };
  const LEVELS = { trace: "debug", debug: "debug", info: "info", notice: "info", warn: "warn", warning: "warn", error: "error", err: "error", fatal: "error", panic: "error", critical: "error" };
  const levelOf = (s) => LEVELS[String(s || "").toLowerCase()] || null;

  // Short local time for an RFC 3339 stamp; the full stamp stays in the tooltip.
  function stamp(ts) {
    const d = new Date(ts);
    const t = isNaN(d) ? ts : d.toLocaleTimeString(undefined, { hour12: false });
    const s = span("lg-ts", t);
    s.title = ts;
    return s;
  }
  function badge(level) {
    const l = levelOf(level) || "info";
    return span(`lg-lvl lv-${l}`, String(level).toUpperCase().slice(0, 5));
  }

  // ANSI SGR colours to spans.
  const ANSI = /\x1b\[([0-9;]*)m/g;
  function ansi(text, out) {
    let last = 0, cls = [];
    let m;
    ANSI.lastIndex = 0;
    while ((m = ANSI.exec(text))) {
      if (m.index > last) out.append(span(cls.join(" "), text.slice(last, m.index)));
      for (const code of (m[1] || "0").split(";").map(Number)) {
        if (code === 0) cls = [];
        else if (code === 1) cls.push("ansi-b");
        else if ((code >= 30 && code <= 37) || (code >= 90 && code <= 97)) cls = cls.filter((c) => !c.startsWith("ansi-c")).concat(`ansi-c${code % 10}`);
        else if (code === 39) cls = cls.filter((c) => !c.startsWith("ansi-c"));
      }
      last = ANSI.lastIndex;
    }
    if (last < text.length) out.append(span(cls.join(" "), text.slice(last)));
  }

  // key=value pairs after the message, keys dimmed.
  const KV = /(\w[\w.-]*)=("(?:[^"\\]|\\.)*"|\S+)/g;
  function pairs(rest, out) {
    let last = 0, m;
    KV.lastIndex = 0;
    while ((m = KV.exec(rest))) {
      if (m.index > last) out.append(rest.slice(last, m.index));
      const v = span("lg-val", "");
      plain(unq(m[2]), v);
      out.append(span("lg-key", m[1] + "="), v);
      last = KV.lastIndex;
    }
    if (last < rest.length) out.append(rest.slice(last));
  }
  const unq = (v) => (v.startsWith('"') && v.endsWith('"') ? v.slice(1, -1).replace(/\\"/g, '"') : v);

  // Plain text, token by token: URLs, quoted strings, `code`, [tags], paths, --flags,
  // IP addresses, level words, HTTP status codes and numbers each get their own colour.
  const TOKENS = new RegExp([
    /(https?:\/\/[^\s"'`\]]+)/.source,                                  // 1 url
    /("(?:[^"\\]|\\.)*")/.source,                                      // 2 "quoted"
    /(`[^`]*`)/.source,                                                  // 3 `code`
    /(\[[A-Za-z][\w .:-]{0,30}\])/.source,                                // 4 [tag]
    /((?:~|\.{1,2})?\/(?:[\w.@%+~:-]+\/?)+)/.source,                        // 5 /path
    /((?:^|(?<=\s))--?[A-Za-z][\w-]*)/.source,                              // 6 --flag
    /(\b\d{1,3}(?:\.\d{1,3}){3}(?::\d+)?\b)/.source,                         // 7 ip:port
    /\b(ERROR|ERR|FATAL|PANIC|CRITICAL|WARN|WARNING|INFO|DEBUG|TRACE)\b/.source, // 8 level word
    /(?<=" )([1-5]\d\d)(?= )/.source,                                     // 9 http status
    /(\b\d+(?:\.\d+)?(?:ms|µs|ns|s|m|h|%|[KMGT]i?B)?\b)/.source,             // 10 number
  ].join("|"), "g");
  const TOKEN_CLASS = { 1: "tk-url", 2: "tk-str", 3: "tk-code", 4: "tk-tag", 5: "tk-path", 6: "tk-flag", 7: "tk-ip", 10: "tk-num" };
  function plain(text, out) {
    let last = 0, m;
    TOKENS.lastIndex = 0;
    while ((m = TOKENS.exec(text))) {
      if (!m[0]) { TOKENS.lastIndex++; continue; }
      if (m.index > last) out.append(text.slice(last, m.index));
      const g = m.findIndex((v, i) => i > 0 && v !== undefined);
      if (g === 8) out.append(span(`lg-word lv-${levelOf(m[8])}`, m[8]));
      else if (g === 9) out.append(span(`lg-http h${m[9][0]}`, m[9]));
      else out.append(span(TOKEN_CLASS[g] || "", m[0]));
      last = m.index + m[0].length;
    }
    if (last < text.length) out.append(text.slice(last));
  }
  // A message: tokens coloured inside a span carrying the level's tone.
  function message(text, level) {
    const s = span(`lg-msg lv-${levelOf(level) || "info"}-text`, "");
    plain(text, s);
    return s;
  }

  const LOGFMT = /^time="([^"]+)"\s+level=(\w+)\s+msg=("(?:[^"\\]|\\.)*"|\S+)\s*(.*)$/;
  const DOCKER_TS = /^(\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d(?:\.\d+)?(?:Z|[+-]\d\d:\d\d))\s(.*)$/;

  // Returns a <div> for one log line, with q (if any) marked.
  function line(text, q, isErr) {
    const div = document.createElement("div");
    div.className = "lg" + (isErr ? " lg-stderr" : "");
    let m;
    let body = text;
    if ((m = DOCKER_TS.exec(body))) {
      div.append(stamp(m[1]), " ");
      body = m[2];
    }
    if (body.includes("\x1b[")) {
      ansi(body, div);
    } else if ((m = LOGFMT.exec(body))) {
      div.append(stamp(m[1]), " ", badge(m[2]), " ", message(unq(m[3]), m[2]));
      if (m[4]) { div.append(" "); pairs(m[4], div); }
    } else if (body.startsWith("{") && (m = tryJSON(body)) && (m.msg || m.message)) {
      const lvl = m.level || m.severity || m.lvl || "info";
      if (m.time || m.ts || m.timestamp) div.append(stamp(String(m.time || m.ts || m.timestamp)), " ");
      div.append(badge(lvl), " ", message(String(m.msg || m.message), lvl));
      for (const [k, v] of Object.entries(m)) {
        if (["msg", "message", "level", "severity", "lvl", "time", "ts", "timestamp"].includes(k)) continue;
        div.append(" ", span("lg-key", k + "="), span("lg-val", typeof v === "string" ? v : JSON.stringify(v)));
      }
    } else {
      plain(body, div);
    }
    if (q) mark(div, q);
    return div;
  }
  function tryJSON(s) { try { const v = JSON.parse(s); return v && typeof v === "object" ? v : null; } catch { return null; } }

  // Wraps every case-insensitive match of q in text nodes with <mark>.
  function mark(root, q) {
    const needle = q.toLowerCase();
    const walker = document.createTreeWalker(root, NodeFilter.SHOW_TEXT);
    const nodes = [];
    while (walker.nextNode()) nodes.push(walker.currentNode);
    for (const n of nodes) {
      const t = n.nodeValue, lower = t.toLowerCase();
      let i = lower.indexOf(needle);
      if (i < 0) continue;
      const frag = document.createDocumentFragment();
      let last = 0;
      while (i >= 0) {
        frag.append(t.slice(last, i));
        const mk = document.createElement("mark");
        mk.textContent = t.slice(i, i + q.length);
        frag.append(mk);
        last = i + q.length;
        i = lower.indexOf(needle, last);
      }
      frag.append(t.slice(last));
      n.replaceWith(frag);
    }
  }

  window.logfmt = { line };
})();
