import { describe, expect, it } from 'vitest';
import { lineDiff } from './lineDiff';

describe('line diff', () => {
 it('marks changed lines with original numbers and collapses unchanged runs', () => {
  const before = Array.from({ length: 20 }, (_, i) => `line ${i + 1}`).join('\n') + '\n';
  const after = before.replace('line 10\n', 'line ten\n');
  const diff = lineDiff(before, after);
  expect(diff.filter(l => l.kind === 'remove')).toEqual([{ kind: 'remove', text: 'line 10', oldLine: 10 }]);
  expect(diff.filter(l => l.kind === 'add')).toEqual([{ kind: 'add', text: 'line ten', newLine: 10 }]);
  expect(diff.filter(l => l.kind === 'skip').length).toBe(2);
  expect(diff.find(l => l.text === 'line 7')).toBeTruthy();
  expect(diff.find(l => l.text === 'line 2')).toBeFalsy();
 });
 it('shows created and deleted files as all added or removed', () => {
  expect(lineDiff('', 'a\nb\n').map(l => l.kind)).toEqual(['add', 'add']);
  expect(lineDiff('a\nb\n', '').map(l => l.kind)).toEqual(['remove', 'remove']);
 });
});
