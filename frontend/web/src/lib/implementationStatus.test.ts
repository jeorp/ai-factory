import { describe, expect, it } from 'vitest';
import { implementationCILink, implementationCIMessageLink } from './implementationStatus';

describe('CI links', () => {
 const commit = 'd'.repeat(40);
 const url = `https://github.com/a/b/commit/${commit}/checks`;
 it('links to the exact published commit, including historical chat links', () => {
  expect(implementationCILink('a/b', { commit })).toBe(url);
  expect(implementationCIMessageLink('a/b', `[CIを確認](${url})`)).toBe(url);
 });
 it('does not turn arbitrary messages or repositories into CI links', () => {
  expect(implementationCILink('a/b', { commit: 'main' })).toBe('');
  expect(implementationCILink('a/../b', { commit })).toBe('');
  for (const text of [`[CIを確認](${url.replace('github.com', 'evil.test')})`, `[CIを確認](${url.replace('a/b/', 'other/repo/')})`, `[CIを確認](${url})\nextra`, `[CIを確認](${url}?redirect=bad)`]) expect(implementationCIMessageLink('a/b', text)).toBe('');
 });
});
