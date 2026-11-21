// Compares two recordings made by slicer.scenario.js and lists every difference.
//   node tests/ui/compare.js <before.json> <after.json>
const fs = require('fs');
const [fa, fb] = process.argv.slice(2);
if (!fa || !fb) { console.error('usage: node compare.js <before.json> <after.json>'); process.exit(2); }
const a = JSON.parse(fs.readFileSync(fa, 'utf8')), b = JSON.parse(fs.readFileSync(fb, 'utf8'));
function diff(x, y, path, out) {
  if (x === y) return;
  if (x === null || y === null || typeof x !== typeof y || Array.isArray(x) !== Array.isArray(y)) { out.push([path, x, y]); return; }
  if (Array.isArray(x)) { if (x.length !== y.length) { out.push([path + '.length', x.length, y.length]); return; } x.forEach((v, i) => diff(v, y[i], `${path}[${i}]`, out)); return; }
  if (typeof x === 'object') { for (const k of new Set([...Object.keys(x), ...Object.keys(y)])) { if (!(k in x) || !(k in y)) out.push([path + '.' + k, x[k], y[k]]); else diff(x[k], y[k], path + '.' + k, out); } return; }
  out.push([path, x, y]);
}
const show = (v) => JSON.stringify(v === undefined ? '(missing)' : v).slice(0, 110);
let total = 0;
const n = Math.max(a.steps.length, b.steps.length);
for (let i = 0; i < n; i++) {
  const sa = a.steps[i], sb = b.steps[i];
  if (!sa || !sb) { total++; console.log(`step ${i + 1}: only in one recording (${(sa || sb).name})`); continue; }
  const out = [];
  if (sa.name !== sb.name) out.push(['name', sa.name, sb.name]);
  diff(sa.s, sb.s, '', out);
  if (out.length) { total += out.length; console.log(`step ${i + 1} "${sa.name}": ${out.length} difference(s)`); for (const [p, x, y] of out.slice(0, 6)) console.log(`     ${p}: ${show(x)}  ->  ${show(y)}`); }
}
for (const k of ['projects', 'thumb', 'pm', 'pages', 'errs', 'log']) {
  const out = []; diff(a[k], b[k], k, out);
  if (out.length) { total += out.length; console.log(`${k}:`); for (const [p, x, y] of out.slice(0, 8)) console.log(`     ${p}: ${show(x)}  ->  ${show(y)}`); }
}
console.log(`${n} steps compared, ${total} difference(s)`);
process.exit(total ? 1 : 0);
