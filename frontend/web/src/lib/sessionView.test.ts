import { describe, expect, it } from 'vitest';
import { canResume, defaultReviewPolicy, inputLimitBytes } from './sessionView';
import type { Review, Session } from '../types';

const session = (patch: Partial<Session>): Session => ({ state: 'done', messages: [], target: { kind: 'repository' }, ...patch }) as Session;

describe('session view helpers', () => {
 it('offers resume for interrupted runs with saved progress', () => {
  expect(canResume(session({ state: 'failed', messages: [{} as Session['messages'][number]] }))).toBe(true);
  expect(canResume(session({ state: 'user_wait', runKind: 'review' }))).toBe(true);
  expect(canResume(session({ state: 'stopped' }))).toBe(false);
  expect(canResume(session({ state: 'running', runKind: 'implement' }))).toBe(false);
 });
 it('offers resume for a finished pull request session whose review stopped midway', () => {
  const target = { kind: 'pull', repository: 'o/r', commit: '', number: 1 };
  expect(canResume(session({ target, review: { phase: 'diff' } as Review }))).toBe(true);
  expect(canResume(session({ target, review: { phase: 'done' } as Review }))).toBe(false);
  expect(canResume(session({ target }))).toBe(false);
 });
 it('uses the review input limit only for reviews', () => {
  expect(inputLimitBytes(session({ runKind: 'review' }), defaultReviewPolicy)).toBe(262144);
  expect(inputLimitBytes(session({}), defaultReviewPolicy)).toBe(24576);
  expect(inputLimitBytes(undefined, defaultReviewPolicy)).toBe(24576);
 });
});
