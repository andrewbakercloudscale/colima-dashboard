// Small SVG charts for Colima Dashboard: ring gauge, donut, area/line chart with a hover crosshair,
// and sparkline. No library: the page stays one binary with no third-party code.
"use strict";

const SVGNS = "http://www.w3.org/2000/svg";

function svg(tag, attrs, ...children) {
  const n = document.createElementNS(SVGNS, tag);
  for (const [k, v] of Object.entries(attrs || {})) {
    if (v === undefined || v === null) continue;
    if (k === "style") for (const [sk, sv] of Object.entries(v)) n.style.setProperty(sk, sv);
    else n.setAttribute(k, String(v));
  }
  for (const c of children.flat()) if (c) n.append(c);
  return n;
}

// Categorical slots, in fixed order (validated palette). Index 0..7; "other" is gray.
const SERIES = ["--series-1", "--series-2", "--series-3", "--series-4", "--series-5", "--series-6", "--series-7", "--series-8"];
const seriesColor = (i) => (i < 0 ? "var(--series-other)" : `var(${SERIES[i % SERIES.length]})`);

// ring: a gauge from 0..100 with the value in the middle.
function ringGauge(pct, size = 116) {
  const r = size / 2 - 9;
  const c = 2 * Math.PI * r;
  const v = Math.max(0, Math.min(100, pct || 0));
  const arc = svg("circle", {
    cx: size / 2, cy: size / 2, r, fill: "none", "stroke-width": 10, "stroke-linecap": "round",
    "stroke-dasharray": `${(v / 100) * c} ${c}`, transform: `rotate(-90 ${size / 2} ${size / 2})`,
    class: "ring-arc",
  });
  arc.style.setProperty("stroke", v >= 85 ? "var(--ring-hot)" : v >= 60 ? "var(--ring-warm)" : "var(--ring-ok)");
  return svg("svg", { viewBox: `0 0 ${size} ${size}`, width: size, height: size, class: "ring", "aria-hidden": "true" },
    svg("circle", { cx: size / 2, cy: size / 2, r, fill: "none", "stroke-width": 10, class: "ring-track" }),
    arc);
}

// donut: segments [{label, value, color}] with a 2px surface gap between them.
function donut(segments, size = 168, center) {
  const total = segments.reduce((a, s) => a + s.value, 0);
  const r = size / 2 - 14;
  const c = 2 * Math.PI * r;
  const g = svg("g", { transform: `rotate(-90 ${size / 2} ${size / 2})` });
  g.append(svg("circle", { cx: size / 2, cy: size / 2, r, fill: "none", "stroke-width": 22, class: "ring-track" }));
  let off = 0;
  if (total > 0) {
    for (const s of segments) {
      const len = (s.value / total) * c;
      if (len <= 0) continue;
      const gap = segments.length > 1 ? Math.min(2, len / 2) : 0;
      const seg = svg("circle", {
        cx: size / 2, cy: size / 2, r, fill: "none", "stroke-width": 22,
        "stroke-dasharray": `${Math.max(len - gap, 0.5)} ${c}`, "stroke-dashoffset": -off,
      });
      seg.style.setProperty("stroke", s.color);
      seg.append(svg("title", {}, document.createTextNode(`${s.label}: ${s.text || s.value}`)));
      g.append(seg);
      off += len;
    }
  }
  const node = svg("svg", { viewBox: `0 0 ${size} ${size}`, width: size, height: size, class: "donut", role: "img" }, g);
  if (center) {
    node.append(svg("text", { x: size / 2, y: size / 2 + 2, "text-anchor": "middle", class: "donut-big" }, document.createTextNode(center[0])));
    node.append(svg("text", { x: size / 2, y: size / 2 + 22, "text-anchor": "middle", class: "donut-small" }, document.createTextNode(center[1])));
  }
  return node;
}

function niceMax(v) {
  if (!(v > 0)) return 1;
  const p = Math.pow(10, Math.floor(Math.log10(v)));
  for (const m of [1, 2, 2.5, 5, 10]) if (m * p >= v) return m * p;
  return 10 * p;
}

function clock(ms, seconds) {
  return new Date(ms).toLocaleTimeString([], seconds ? { hour: "2-digit", minute: "2-digit", second: "2-digit" } : { hour: "2-digit", minute: "2-digit" });
}

// timeChart: area (stacked) or line chart over time with gridlines, time axis, hover tooltip and
// a legend. series: [{name, color, values: [number|null]}]; times: [ms]. One y axis only.
function timeChart({ times, series, domain, stacked = false, fmt = (v) => v.toFixed(1), yMax, height = 190, empty = "Collecting…", gapMs = 6500 }) {
  const wrap = document.createElement("div");
  wrap.className = "tchart";
  const W = 600, H = height, L = 46, R = 8, T = 10, B = 24;
  if (times.length < 2) {
    wrap.append(Object.assign(document.createElement("p"), { className: "muted small chart-empty", textContent: empty }));
    return wrap;
  }
  const n = times.length;
  // The x axis spans the chosen range, not just the data, so a fresh page fills in from the right.
  const t0 = domain ? Math.min(domain[0], times[0]) : times[0];
  const t1 = domain ? Math.max(domain[1], times[n - 1]) : times[n - 1];
  const fine = t1 - t0 <= 6 * 60e3;
  const days = t1 - t0 > 36 * 3600e3; // long ranges label dates, not times
  const label = (t) => days ? new Date(t).toLocaleDateString([], { day: "numeric", month: "short" }) : clock(t, fine);
  const x = (t) => L + ((t - t0) / Math.max(1, t1 - t0)) * (W - L - R);
  // stacked tops per series
  const tops = series.map(() => new Array(n).fill(0));
  let peak = 0;
  for (let i = 0; i < n; i++) {
    let acc = 0;
    series.forEach((s, k) => {
      const v = s.values[i] ?? 0;
      acc = stacked ? acc + v : v;
      tops[k][i] = acc;
      peak = Math.max(peak, acc);
    });
  }
  const max = yMax ?? niceMax(peak * 1.1);
  const y = (v) => T + (1 - Math.min(v, max) / max) * (H - T - B);
  const root = svg("svg", { viewBox: `0 0 ${W} ${H}`, class: "chart", preserveAspectRatio: "none", role: "img" });
  // grid + y labels (recessive)
  for (const f of [0, 0.5, 1]) {
    const v = max * f;
    root.append(svg("line", { x1: L, x2: W - R, y1: y(v), y2: y(v), class: "grid" }));
    root.append(svg("text", { x: L - 6, y: y(v) + 4, "text-anchor": "end", class: "axis" }, document.createTextNode(fmt(v))));
  }
  // time labels: start, middle, end
  for (const f of [0, 0.25, 0.5, 0.75, 1]) {
    const t = t0 + (t1 - t0) * f;
    root.append(svg("text", { x: x(t), y: H - 6, "text-anchor": f === 0 ? "start" : f === 1 ? "end" : "middle", class: "axis" }, document.createTextNode(label(t))));
  }
  // gaps: a break where samples are more than 3 intervals apart
  const gapAt = (i) => i > 0 && times[i] - times[i - 1] > gapMs;
  series.forEach((s, k) => {
    const color = s.color;
    let line = "", area = "";
    let segStart = 0;
    const flush = (end) => {
      if (end - segStart < 1) return;
      let d = "";
      for (let i = segStart; i < end; i++) d += `${i === segStart ? "M" : "L"}${x(times[i]).toFixed(1)},${y(tops[k][i]).toFixed(1)}`;
      line += d;
      if (stacked || s.fill) {
        let back = "";
        for (let i = end - 1; i >= segStart; i--) {
          const base = stacked && k > 0 ? tops[k - 1][i] : 0;
          back += `L${x(times[i]).toFixed(1)},${y(base).toFixed(1)}`;
        }
        area += d + back + "Z";
      }
    };
    for (let i = 1; i <= n; i++) if (i === n || gapAt(i)) { flush(i); segStart = i; }
    if (area) {
      const a = svg("path", { d: area, class: "area" });
      a.style.setProperty("fill", color);
      root.append(a);
    }
    const p = svg("path", { d: line, class: "line" });
    p.style.setProperty("stroke", color);
    root.append(p);
  });
  // hover layer
  const cross = svg("line", { y1: T, y2: H - B, class: "cross", visibility: "hidden" });
  const hit = svg("rect", { x: L, y: T, width: W - L - R, height: H - T - B, class: "hit" });
  root.append(cross, hit);
  const tip = document.createElement("div");
  tip.className = "tip";
  tip.hidden = true;
  hit.addEventListener("pointermove", (e) => {
    const box = root.getBoundingClientRect();
    const px = ((e.clientX - box.left) / box.width) * W;
    const t = t0 + ((px - L) / (W - L - R)) * (t1 - t0);
    let i = 0;
    while (i < n - 1 && Math.abs(times[i + 1] - t) < Math.abs(times[i] - t)) i++;
    cross.setAttribute("x1", x(times[i]));
    cross.setAttribute("x2", x(times[i]));
    cross.setAttribute("visibility", "visible");
    tip.replaceChildren();
    const head = document.createElement("strong");
    head.textContent = t1 - t0 > 20 * 3600e3 ? new Date(times[i]).toLocaleString([], { day: "numeric", month: "short", hour: "2-digit", minute: "2-digit" }) : new Date(times[i]).toLocaleTimeString();
    tip.append(head);
    for (const s of [...series].reverse()) {
      const row = document.createElement("div");
      const sw = document.createElement("span");
      sw.className = "sw";
      sw.style.setProperty("background", s.color);
      const v = s.values[i];
      row.append(sw, `${s.name}: ${v === null || v === undefined ? "–" : fmt(v)}`);
      tip.append(row);
    }
    tip.hidden = false;
    const left = ((x(times[i]) / W) * box.width);
    tip.style.left = `${Math.min(left + 12, box.width - 180)}px`;
  });
  hit.addEventListener("pointerleave", () => { cross.setAttribute("visibility", "hidden"); tip.hidden = true; });
  wrap.append(root, tip);
  if (series.length > 1) wrap.append(legend(series.map((s) => ({ label: s.name, color: s.color }))));
  return wrap;
}

function legend(items) {
  const box = document.createElement("div");
  box.className = "legend";
  for (const it of items) {
    const row = document.createElement("span");
    const sw = document.createElement("span");
    sw.className = "sw";
    sw.style.setProperty("background", it.color);
    row.append(sw, it.label);
    box.append(row);
  }
  return box;
}

// sparkline: one series, no axes, for table cells.
function sparkline(values, color, max) {
  const W = 120, H = 28;
  const vals = values.map((v) => v ?? 0);
  if (vals.length < 2) return svg("svg", { viewBox: `0 0 ${W} ${H}`, class: "spark" });
  const m = max ?? niceMax(Math.max(...vals) * 1.1);
  const step = W / (vals.length - 1);
  let d = "";
  vals.forEach((v, i) => { d += `${i ? "L" : "M"}${(i * step).toFixed(1)},${(H - 2 - (Math.min(v, m) / m) * (H - 4)).toFixed(1)}`; });
  const area = svg("path", { d: `${d}L${W},${H}L0,${H}Z`, class: "area" });
  area.style.setProperty("fill", color);
  const line = svg("path", { d, class: "line" });
  line.style.setProperty("stroke", color);
  return svg("svg", { viewBox: `0 0 ${W} ${H}`, class: "spark", preserveAspectRatio: "none", "aria-hidden": "true" }, area, line);
}

// runBars: one bar per run or build, height its duration, colour how it ended. points:
// [{t, seconds, outcome}], oldest first. Each bar has a tooltip.
const OUTCOME_COLORS = { pass: "var(--ok)", fail: "var(--bad)", oom: "#9333ea", stopped: "var(--series-other)" };
function runBars(points, fmt) {
  const W = 160, H = 32, gap = 2;
  const root = svg("svg", { viewBox: `0 0 ${W} ${H}`, class: "runbars", preserveAspectRatio: "none", role: "img" });
  if (!points.length) return root;
  const max = Math.max(...points.map((p) => p.seconds || 0), 0.001);
  const bw = Math.max(2, (W - gap * (points.length - 1)) / Math.max(points.length, 12));
  points.forEach((p, i) => {
    const h = Math.max(2, ((p.seconds || 0) / max) * (H - 2));
    const r = svg("rect", { x: i * (bw + gap), y: H - h, width: bw, height: h, rx: 1 });
    r.style.setProperty("fill", OUTCOME_COLORS[p.outcome] || OUTCOME_COLORS.stopped);
    r.append(svg("title", {}, document.createTextNode(`${new Date(p.t).toLocaleString([], { day: "numeric", month: "short", hour: "2-digit", minute: "2-digit" })}: ${p.outcome}, ${fmt(p.seconds || 0)}`)));
    root.append(r);
  });
  return root;
}

window.Charts = { ringGauge, donut, timeChart, sparkline, legend, seriesColor, niceMax, runBars, OUTCOME_COLORS };
