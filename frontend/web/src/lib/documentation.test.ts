import { describe, expect, it } from 'vitest';
import { documentationFileURL, documentationLimitations, documentationMarkdown } from './documentation';
import type { Documentation } from '../types';

const doc = (): Documentation => ({ request: 'ログイン機能', scope: 'feature', repository: 'a/b', commit: 'a'.repeat(40), status: 'ready', sources: [], limitations: ['テストは未実行です。'], createdAt: '', result: { title: 'ログインの実装ガイド', summary: '認証の処理を辿ります。', sections: [{ heading: '処理の流れ', body: '1. 画面からAPIへ\n2. セッションを保存', paths: ['src/login view.ts'] }], unverified: ['テストは未実行です。', '外部サービスの応答は未確認です。'] } });
describe('implementation guide export', () => {
 it('exports scope, fixed commit, flow, code links and uncertainty', () => {
  const d = doc(); const markdown = documentationMarkdown(d);
  expect(markdown).toContain('# ログインの実装ガイド');
  expect(markdown).toContain('資料の範囲: 機能・画面');
  expect(markdown).toContain('1. 画面からAPIへ\n2. セッションを保存');
  expect(markdown).toContain(`/blob/${d.commit}/src/login%20view.ts`);
  expect(markdown).toContain('外部サービスの応答は未確認です。');
  expect(documentationLimitations(d)).toHaveLength(2);
 });
 it('does not turn invalid paths or repositories into links', () => {
  for (const path of ['../secret', '/etc/passwd', 'https://example.com/', 'file.ts#L1', 'file.ts?x=1']) expect(documentationFileURL(doc(), path)).toBe('#');
  expect(documentationFileURL({ ...doc(), repository: 'evil.invalid/x/y' }, 'file.ts')).toBe('#');
  expect(documentationFileURL({ ...doc(), commit: 'main' }, 'file.ts')).toBe('#');
 });
 it('keeps repository guides distinct and omits invalid links from the export', () => {
  const d = doc(); d.scope = 'repository'; d.result!.sections[0]!.paths.push('../secret');
  const markdown = documentationMarkdown(d);
  expect(markdown).toContain('資料の範囲: リポジトリ全体');
  expect(markdown).not.toContain('../secret');
  expect(documentationMarkdown({ ...d, result: undefined })).toBe('');
 });
});
