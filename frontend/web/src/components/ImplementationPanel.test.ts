import { describe, expect, it, vi } from 'vitest';
import { createSSRApp } from 'vue';
import { renderToString } from '@vue/server-renderer';
import ImplementationPanel from './ImplementationPanel.vue';
import type { Implementation } from '../types';

vi.mock('../lib/auth', () => ({ api: vi.fn() }));

const implementation = (status: Implementation['status']): Implementation => ({ request: '戻り値を修正', status, version: 1, baseBranch: 'main', baseCommit: 'c'.repeat(40), nextStep: 0, changes: [], unverified: [], summary: '' });
const render = (status: Implementation['status'], state: string, canRun = true) => renderToString(createSSRApp(ImplementationPanel, { sessionId: 'session', repository: 'a/b', implementation: implementation(status), sessionState: state, running: state === 'running', canRun, connected: true }));

describe('implementation recovery', () => {
 it('shows the reason a change was blocked even when no file was generated', async () => {
  const impl = implementation('editing'); impl.unverified = ['CIログが取得できず原因を確認できません'];
  const html = await renderToString(createSSRApp(ImplementationPanel, { sessionId: 'session', repository: 'a/b', implementation: impl, sessionState: 'failed', running: false, canRun: true, connected: true }));
  expect(html).toContain('CIログが取得できず原因を確認できません');
  expect(html).toContain('未確認事項');
 });
 it('shows a stopped phase and resume action after a format error', async () => {
  const html = await render('planning', 'failed');
  expect(html).toContain('計画作成を中断');
  expect(html).not.toContain('計画を作成中');
  expect(html).toContain('保存済みの状態から再開');
 });
 it('explains why an expired API key prevents resuming', async () => {
  const html = await render('editing', 'auth_wait', false);
  expect(html).toContain('APIキーを接続してから再開');
  expect(html).toMatch(/<button[^>]*disabled[^>]*>保存済みの状態から再開/);
 });
 it('does not offer resume during a run or when waiting for plan approval', async () => {
  expect(await render('planning', 'running')).not.toContain('保存済みの状態から再開');
  expect(await render('plan_ready', 'done')).not.toContain('保存済みの状態から再開');
 });
});


describe('implementation verification and CI results', () => {
 const renderImpl=(impl: Implementation)=>renderToString(createSSRApp(ImplementationPanel,{sessionId:'session',repository:'a/b',implementation:impl,sessionState:'done',running:false,canRun:true,connected:true}));
 it('blocks publication and avoids a clean assessment when uncertainty remains',async()=>{
  const impl=implementation('verification_blocked');impl.verification={issues:[],unverified:['Localeの引き渡しを未確認']};
  const html=await renderImpl(impl);
  expect(html).toContain('公開停止');expect(html).toContain('Localeの引き渡しを未確認');expect(html).toContain('未確認事項を再調査');expect(html).not.toContain('不整合の指摘はありません');expect(html).not.toContain('Draft PRを作成</button>');
 });
 it('offers a repair for a failed CI result tied to the published commit',async()=>{
  const impl=implementation('published');impl.ci={commit:'d'.repeat(40),state:'failed',checks:[{name:'backend',status:'completed',conclusion:'failure',jobId:42}],unverified:[]};
  const html=await renderImpl(impl);expect(html).toContain('CI失敗・修正が必要');expect(html).toContain('dddddddddddd');expect(html).toContain('CIの失敗を修正');expect(html).toContain('CIを再確認');
 });
 it('keeps pending CI separate from success',async()=>{
  const impl=implementation('published');impl.ci={commit:'d'.repeat(40),state:'pending',checks:[],unverified:[]};
  const html=await renderImpl(impl);expect(html).toContain('CI待ち');expect(html).not.toContain('CI成功');expect(html).not.toContain('CIの失敗を修正');
 });
});
