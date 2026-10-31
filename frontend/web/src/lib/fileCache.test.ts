import { describe, expect, it } from 'vitest';
import { fileTTL, findSaved, maxCacheBytes, maxFileBytes, retainedFiles, type SavedFile } from './fileCache';

const now = 10000000;
function file(path: string, savedAt = now, content = 'source'): SavedFile {
 return { owner: 'user', session: 'session', repository: 'owner/repo', commit: 'a'.repeat(40), path, content, url: '', cached: true, savedAt };
}
describe('browser source cache policy', () => {
 it('rejects expired, future-dated and oversized UTF-8 content; accepts empty files', () => {
  const valid = file('valid.ts'); const empty = file('empty.ts', now, '');
  expect(retainedFiles([file('expired', now - fileTTL), file('future', now + 1), file('large', now, 'あ'.repeat(maxFileBytes / 3 + 1)), valid, empty], now)).toEqual([valid, empty]);
 });
 it('evicts older files to stay within the total storage budget', () => {
  const files = Array.from({ length: 150 }, (_, index) => file(`${index}.ts`, now - index, 'x'.repeat(maxFileBytes)));
  const kept = retainedFiles(files, now);
  expect(kept.length).toBeLessThan(files.length);
  expect(kept[0]?.path).toBe('0.ts');
  expect(kept.some(file => file.path === '149.ts')).toBe(false);
  expect(kept.reduce((size, file) => size + new TextEncoder().encode(JSON.stringify(file)).length, 0)).toBeLessThanOrEqual(maxCacheBytes);
 });
 it('never reuses a different commit or repository, or an expired file', () => {
  const saved = file('src/a.ts');
  expect(findSaved([saved], saved, now)).toBe(saved);
  expect(findSaved([saved], { ...saved, commit: 'b'.repeat(40) }, now)).toBeUndefined();
  expect(findSaved([saved], { ...saved, repository: 'other/repo' }, now)).toBeUndefined();
  expect(findSaved([saved], saved, now + fileTTL)).toBeUndefined();
 });
});
