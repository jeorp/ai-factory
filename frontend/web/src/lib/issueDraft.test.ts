import { describe, expect, it } from 'vitest';
import { issueDraft } from './issueDraft';
import type { Finding } from '../types';

const finding: Finding = { severity: 'P1', path: 'src/a.ts', line: 42, side: 'RIGHT', title: '値が失われる', problem: '問題', impact: '影響', evidence: '- old\n+ new', suggestion: '修正' };

describe('issue draft', () => {
 it('includes the finding and the reviewed PR and code in the prefilled URL', () => {
  const result = issueDraft(finding, 'owner/repo', 7, 'https://github.com/owner/repo/blob/abc/src/a.ts#L42');
  const url = new URL(result.fullUrl);
  expect(url.pathname).toBe('/owner/repo/issues/new');
  expect(url.searchParams.get('title')).toBe('[P1] 値が失われる');
  expect(url.searchParams.get('body')).toContain('https://github.com/owner/repo/pull/7');
  expect(url.searchParams.get('body')).toContain('https://github.com/owner/repo/blob/abc/src/a.ts#L42');
  expect(url.searchParams.get('body')).toContain('- old\n+ new');
 });
 it('keeps the complete body available when the URL would be too long', () => {
  const result = issueDraft({ ...finding, evidence: 'あ'.repeat(3000) }, 'owner/repo', 7, 'https://github.com/owner/repo/blob/abc/src/a.ts');
  expect(result.fullUrl).toBe('');
  expect(result.body).toContain('あ'.repeat(3000));
  expect(new URL(result.fallbackUrl).searchParams.get('title')).toBe('[P1] 値が失われる');
 });
});
