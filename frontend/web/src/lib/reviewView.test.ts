import { describe, expect, it } from 'vitest';
import { findingLink, processedFileCount, reviewFileStatus, reviewPhaseLabel, reviewScopeNote, unverifiedItems } from './reviewView';
import { safeGitHubLink } from './github';
import type { Finding, Review } from '../types';

const file = (path: string, done: boolean, previousPath = '') => ({ path, previousPath, status: 'modified', index: 0, offset: 0, done });
const review = (patch: Partial<Review> = {}): Review => ({
 title: 't', body: '', base: 'b'.repeat(40), head: 'h'.repeat(40), totalFiles: 3, phase: 'diff', crossCursor: 0, crossSummary: '',
 steps: [{ directory: 'a', state: 'done', summary: '', files: [file('a/x.ts', true), file('a/new name.ts', false, 'a/old.ts')] }, { directory: 'b', state: 'pending', summary: '', files: [file('b/y.ts', true)] }],
 findings: [], unverified: [], ...patch,
});
const finding = (patch: Partial<Finding>): Finding => ({ severity: 'P1', path: 'a/x.ts', line: 3, side: 'RIGHT', title: '', problem: '', impact: '', evidence: '', suggestion: '', ...patch });

describe('review view helpers', () => {
 it('counts processed files across steps', () => {
  expect(processedFileCount(review())).toBe(2);
 });
 it('labels phases, scopes and file progress', () => {
  expect(reviewPhaseLabel('cross')).toBe('ファイル間の整合性を確認中');
  expect(reviewPhaseLabel('new_phase')).toBe('new_phase');
  expect(reviewScopeNote(review({ scope: 'frontend' }))).toContain('backend/ を除外');
  expect(reviewScopeNote(review({ scope: 'all' }))).toBe('');
  expect(reviewFileStatus({ ...file('x', false), offset: 10 })).toBe('差分の途中まで処理済み');
  expect(reviewFileStatus({ ...file('x', true), excluded: true })).toBe('指定により対象外（AI確認なし）');
 });
 it('orders unverified items by priority and hides unscored ones until done', () => {
  const priorities = { 0: { level: 'low', reason: '' }, 2: { level: 'high', reason: '' } } as Review['priorities'];
  const pending = review({ unverified: ['a', 'b', 'c'], priorities });
  expect(unverifiedItems(pending).map(item => item.text)).toEqual(['c', 'a']);
  expect(unverifiedItems({ ...pending, phase: 'done' }).map(item => item.text)).toEqual(['c', 'a', 'b']);
  expect(unverifiedItems(undefined)).toEqual([]);
 });
 it('links findings to the head, or the base path for removed lines', () => {
  expect(findingLink('o/r', review(), finding({}))).toBe(`https://github.com/o/r/blob/${'h'.repeat(40)}/a/x.ts#L3`);
  expect(findingLink('o/r', review(), finding({ path: 'a/new name.ts', side: 'LEFT', line: 0 }))).toBe(`https://github.com/o/r/blob/${'b'.repeat(40)}/a/old.ts`);
  expect(findingLink('o/r', review(), finding({ path: 'a/new name.ts' }))).toContain('/a/new%20name.ts#L3');
 });
 it('only allows https GitHub links', () => {
  expect(safeGitHubLink('https://github.com/o/r')).toBe('https://github.com/o/r');
  expect(safeGitHubLink('javascript:alert(1)')).toBe('#');
  expect(safeGitHubLink('https://example.com/o/r')).toBe('#');
  expect(safeGitHubLink('not a url')).toBe('#');
 });
});
