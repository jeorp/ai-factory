import { describe, expect, it } from 'vitest';
import { createSSRApp } from 'vue';
import { renderToString } from '@vue/server-renderer';
import DocumentationPanel from './DocumentationPanel.vue';
import type { Documentation } from '../types';

const doc = (status: Documentation['status'] = 'ready'): Documentation => ({ request: 'ログイン', scope: 'feature', repository: 'a/b', commit: 'a'.repeat(40), status, sources: [], limitations: ['コード実行は未確認です。'], createdAt: '', result: status === 'ready' ? { title: 'ログインの実装ガイド', summary: '認証を辿ります。', sections: [{ heading: '処理の流れ', body: '<script>alert(1)</script>\n画面 → API → 保存', paths: ['src/login.ts', '../unsafe'] }], unverified: ['タイムアウトは未確認です。'] } : undefined });
const render = (documentation?: Documentation, extra = {}) => renderToString(createSSRApp(DocumentationPanel, { sessionId: 'session', documentation, running: false, canRun: true, sessionState: 'done', active: true, ...extra }));

describe('implementation guide panel', () => {
 it('starts with a feature-focused form and also offers repository guides', async () => {
  const html = await render();
  expect(html).toContain('理解したい機能・画面');
  expect(html).toContain('リポジトリ全体');
  expect(html).toMatch(/<input(?=[^>]*checked)(?=[^>]*type="radio")[^>]*value="feature"/);
  expect(html).toMatch(/<button[^>]*disabled[^>]*>[\s\S]*?資料を作成/);
 });
 it('renders flow and safe pinned references, escapes model HTML and keeps uncertainty visible', async () => {
  const html = await render(doc());
  expect(html).toContain('ログインの実装ガイド');
  expect(html).toContain('画面 → API → 保存');
  expect(html).toContain(`/blob/${'a'.repeat(40)}/src/login.ts`);
  expect(html).not.toContain('../unsafe');
  expect(html).not.toContain('<script>alert');
  expect(html).toContain('&lt;script&gt;');
  expect(html).toContain('タイムアウトは未確認です。');
  expect(html).toContain('コード実行は未確認です。');
  expect(html).toContain('Markdownで保存');
 });
 it('offers resume only for interrupted documentation research', async () => {
  expect(await render(doc('researching'), { sessionState: 'stopped' })).toContain('保存済みの調査から再開');
  expect(await render(doc('researching'), { sessionState: 'running', running: true })).not.toContain('保存済みの調査から再開');
  expect(await render(doc('researching'), { sessionState: 'failed', active: false })).not.toContain('保存済みの調査から再開');
  expect(await render(doc(), { sessionState: 'failed' })).not.toContain('保存済みの調査から再開');
 });
 it('disables resuming without a connection or usable API key', async () => {
  const html = await render(doc('researching'), { sessionState: 'auth_wait', canRun: false });
  expect(html).toMatch(/<button[^>]*disabled[^>]*>保存済みの調査から再開/);
 });
 it('explains the call limit and offers recreation without an exhausted resume button', async () => {
  const guide = doc('researching'); guide.limitations.push('documentation_call_limit');
  const html = await render(guide, { sessionState: 'failed' });
  expect(html).toContain('資料作成の上限に達しました');
  expect(html).toContain('対象を絞って資料を作り直してください。');
  expect(html).not.toContain('保存済みの調査から再開');
  expect(html).toContain('理解したい機能・画面');
 });
});
