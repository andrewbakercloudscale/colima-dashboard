// Draws the blog's two benchmark charts as SVG from numbers.json, then PNG (rsvg-convert, 2x).
//   node docs/blog/charts.js
// Each panel has its own scale, so memory in GB and CPU in % never share an axis. Every label is
// measured (roughly) so nothing touches the edge of the image.
const fs = require('fs');
const path = require('path');
const { execFileSync } = require('child_process');

const N = JSON.parse(fs.readFileSync(path.join(__dirname, 'numbers.json'), 'utf8'));
const RD = '#94a3b8', CO = '#0d9488', INK = '#0f172a', MUTED = '#64748b', GRID = '#e2e8f0';
const FONT = "-apple-system, BlinkMacSystemFont, 'Segoe UI', Helvetica, Arial, sans-serif";
const esc = (s) => String(s).replace(/&/g, '&amp;').replace(/</g, '&lt;');
// Rough text width for a sans font: wide enough that labels never collide or clip.
const textW = (s, size) => [...String(s)].reduce((a, c) => a + (/[A-Z%]/.test(c) ? 0.68 : /[ il.,()1]/.test(c) ? 0.32 : 0.56), 0) * size;

function chart({ file, title, note, panels }) {
  const W = 880, PAD = 40, BAR = 20, GAP = 6, ROW = BAR * 2 + GAP + 26;
  const labelW = Math.ceil(Math.max(...panels.flatMap((p) => p.rows.map((r) => textW(r.label, 15))))) + 20;
  const valueW = 80; // room for "44.1 s" after the longest bar
  const x0 = PAD + labelW, plotW = W - PAD - x0 - valueW;
  let y = PAD;
  const out = [];
  out.push(`<text x="${PAD}" y="${y + 22}" font-size="22" font-weight="700" fill="${INK}">${esc(title)}</text>`);
  // legend, right aligned inside the padding
  const lg = [['Rancher Desktop', RD], ['Colima', CO]];
  let lx = W - PAD;
  for (const [name, color] of [...lg].reverse()) {
    lx -= textW(name, 14);
    out.push(`<text x="${lx}" y="${y + 20}" font-size="14" fill="${INK}">${name}</text>`);
    lx -= 20;
    out.push(`<rect x="${lx}" y="${y + 8}" width="14" height="14" rx="3" fill="${color}"/>`);
    lx -= 22;
  }
  y += 56;
  for (const p of panels) {
    if (p.heading) {
      out.push(`<text x="${PAD}" y="${y + 14}" font-size="13" font-weight="700" letter-spacing=".06em" fill="${MUTED}">${esc(p.heading.toUpperCase())}</text>`);
      y += 28;
    }
    const max = Math.max(...p.rows.flatMap((r) => [r.rd, r.co]));
    const top = y;
    for (const r of p.rows) {
      out.push(`<text x="${x0 - 16}" y="${y + BAR + GAP / 2 + 5}" font-size="15" text-anchor="end" fill="${INK}">${esc(r.label)}</text>`);
      [[r.rd, RD], [r.co, CO]].forEach(([v, color], i) => {
        const by = y + i * (BAR + GAP);
        const w = Math.max(4, (v / max) * plotW);
        out.push(`<rect x="${x0}" y="${by}" width="${w.toFixed(1)}" height="${BAR}" rx="5" fill="${color}"/>`);
        out.push(`<text x="${(x0 + w + 8).toFixed(1)}" y="${by + BAR - 5}" font-size="14" fill="${INK}" font-weight="${i ? 700 : 400}">${esc(p.fmt(v))}</text>`);
      });
      y += ROW;
    }
    out.splice(out.length - p.rows.length * 5, 0, `<line x1="${x0}" x2="${x0}" y1="${top - 6}" y2="${y - 20}" stroke="${GRID}" stroke-width="1.5"/>`);
    y += 10;
  }
  y += 4;
  out.push(`<text x="${PAD}" y="${y}" font-size="13" fill="${MUTED}">${esc(note)}</text>`);
  const H = y + PAD - 8;
  const svg = `<svg xmlns="http://www.w3.org/2000/svg" width="${W}" height="${H}" viewBox="0 0 ${W} ${H}" font-family="${FONT}">
<rect width="${W}" height="${H}" fill="#ffffff"/>
${out.join('\n')}
</svg>
`;
  const svgPath = path.join(__dirname, `${file}.svg`);
  fs.writeFileSync(svgPath, svg);
  execFileSync('rsvg-convert', ['-z', '2', '-o', path.join(__dirname, `${file}.png`), svgPath]);
  console.log(file, W, 'x', H);
}

const s = (v) => `${v} s`;
chart({
  file: 'chart-start',
  title: 'Start and stop times',
  note: 'Apple M5, 32 GB, macOS 27.2. Median of 5 runs. Shorter is better.',
  panels: [
    { heading: 'Start', fmt: s, rows: [
      { label: 'Cold start, out of the box', rd: N.rd_cold_defaults_s, co: N.co_cold_defaults_s },
      { label: 'Cold start, matched VMs', rd: N.rd_cold_matched_s, co: N.co_cold_matched_s },
      { label: 'To Kubernetes ready', rd: N.rd_cold_k8s_s, co: N.co_cold_k8s_s },
    ] },
    { heading: 'Stop', fmt: s, rows: [
      { label: 'Stop, matched VMs', rd: N.rd_stop_s, co: N.co_stop_s },
      { label: 'Stop, with Kubernetes', rd: N.rd_stop_k8s_s, co: N.co_stop_k8s_s },
    ] },
  ],
});
chart({
  file: 'chart-idle',
  title: 'What sitting idle costs your Mac',
  note: 'Apple M5, 32 GB, macOS 27.2. Average over 10 minutes idle, sampled every 5 s. Smaller is better.',
  panels: [
    { heading: 'Memory', fmt: (v) => `${v} GB`, rows: [
      { label: 'Matched VMs', rd: N.rd_idle_mem_matched_gb, co: N.co_idle_mem_matched_gb },
      { label: 'With Kubernetes', rd: N.rd_idle_mem_k8s_gb, co: N.co_idle_mem_k8s_gb },
    ] },
    { heading: 'CPU, % of one core', fmt: (v) => `${v}%`, rows: [
      { label: 'Matched VMs', rd: N.rd_idle_cpu_matched, co: N.co_idle_cpu_matched },
      { label: 'With Kubernetes', rd: N.rd_idle_cpu_k8s, co: N.co_idle_cpu_k8s },
    ] },
  ],
});
