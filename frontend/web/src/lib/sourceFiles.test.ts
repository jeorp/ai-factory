import { describe, expect, it } from 'vitest';
import { listedFiles } from './sourceFiles';
import type { Session } from '../types';

const commit = 'a'.repeat(40); const head = 'b'.repeat(40); const base = 'c'.repeat(40);
function session(): Session {
 return { target: { repository: 'owner/repo', commit }, sources: [], messages: [] } as unknown as Session;
}
describe('files listed in chat and reviews', () => {
 it('combines pinned sources and enumerated paths without duplicates or foreign URLs', () => {
  const value = session();
  value.sources = [
   { label: 'code', url: `https://github.com/owner/repo/blob/${head}/src/a.ts` },
   { label: 'wrong repo', url: `https://github.com/other/repo/blob/${head}/private.ts` },
   { label: 'wrong host', url: `https://github.com.evil.test/owner/repo/blob/${head}/private.ts` },
  ];
  value.messages = [
   { role: 'assistant', text: '実装は `src/a.ts:12`、`README.md` と backend/cmd/api/main.go。`../secret.ts` や `image.png` は対象外。' },
   { role: 'user', text: '`user-only.ts`' },
  ] as Session['messages'];
  expect(listedFiles(value)).toEqual([
   { repository: 'owner/repo', commit: head, path: 'src/a.ts' },
   { repository: 'owner/repo', commit, path: 'README.md' },
   { repository: 'owner/repo', commit, path: 'backend/cmd/api/main.go' },
  ]);
 });
 it('uses base for removed files and renamed left-side findings', () => {
  const value = session();
  value.review = { head, base, steps: [{ files: [{ path: 'src/new.ts', previousPath: 'src/old.ts', status: 'renamed' }, { path: 'src/deleted.ts', status: 'removed' }] }], findings: [{ path: 'src/new.ts', side: 'LEFT' }] } as Session['review'];
  expect(listedFiles(value).map(file => [file.path, file.commit])).toEqual([
   ['src/new.ts', head], ['src/deleted.ts', base], ['src/old.ts', base],
  ]);
 });
});
