export type DiffLine = { kind: 'same' | 'add' | 'remove' | 'skip'; text: string; oldLine?: number; newLine?: number };

const MAX_CELLS = 4_000_000;

// Line diff for previewing AI changes before a PR is created. Unchanged runs
// longer than 2*context lines are collapsed. Large files fall back to showing
// the whole old and new content.
export function lineDiff(before: string, after: string, context = 3): DiffLine[] {
 const a = before === '' ? [] : before.replace(/\n$/, '').split('\n');
 const b = after === '' ? [] : after.replace(/\n$/, '').split('\n');
 let start = 0;
 while (start < a.length && start < b.length && a[start] === b[start]) start++;
 let endA = a.length, endB = b.length;
 while (endA > start && endB > start && a[endA - 1] === b[endB - 1]) { endA--; endB--; }
 const midA = a.slice(start, endA), midB = b.slice(start, endB);
 const ops: DiffLine[] = [];
 for (let i = 0; i < start; i++) ops.push({ kind: 'same', text: a[i], oldLine: i + 1, newLine: i + 1 });
 if (midA.length * midB.length > MAX_CELLS) {
  midA.forEach((text, i) => ops.push({ kind: 'remove', text, oldLine: start + i + 1 }));
  midB.forEach((text, i) => ops.push({ kind: 'add', text, newLine: start + i + 1 }));
 } else {
  const n = midA.length, m = midB.length;
  const table = Array.from({ length: n + 1 }, () => new Uint32Array(m + 1));
  for (let i = n - 1; i >= 0; i--) for (let j = m - 1; j >= 0; j--) table[i][j] = midA[i] === midB[j] ? table[i + 1][j + 1] + 1 : Math.max(table[i + 1][j], table[i][j + 1]);
  let i = 0, j = 0;
  while (i < n || j < m) {
   if (i < n && j < m && midA[i] === midB[j]) { ops.push({ kind: 'same', text: midA[i], oldLine: start + i + 1, newLine: start + j + 1 }); i++; j++; }
   else if (j < m && (i === n || table[i][j + 1] >= table[i + 1][j])) { ops.push({ kind: 'add', text: midB[j], newLine: start + j + 1 }); j++; }
   else { ops.push({ kind: 'remove', text: midA[i], oldLine: start + i + 1 }); i++; }
  }
 }
 for (let k = 0; k < a.length - endA; k++) ops.push({ kind: 'same', text: a[endA + k], oldLine: endA + k + 1, newLine: endB + k + 1 });
 const out: DiffLine[] = [];
 for (let k = 0; k < ops.length;) {
  if (ops[k].kind !== 'same') { out.push(ops[k++]); continue; }
  let end = k; while (end < ops.length && ops[end].kind === 'same') end++;
  const head = k === 0 ? 0 : context, tail = end === ops.length ? 0 : context;
  if (end - k > head + tail + 1) {
   out.push(...ops.slice(k, k + head));
   out.push({ kind: 'skip', text: `… ${end - k - head - tail} 行省略 …` });
   out.push(...ops.slice(end - tail, end));
  } else out.push(...ops.slice(k, end));
  k = end;
 }
 return out;
}
