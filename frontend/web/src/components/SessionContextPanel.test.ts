import { describe, expect, it } from 'vitest';
import { createSSRApp } from 'vue';
import { renderToString } from '@vue/server-renderer';
import SessionContextPanel from './SessionContextPanel.vue';
import type { Session } from '../types';

const fixture = (): Session => ({ id:'test', title:'repo', model:'gpt-5-mini', state:'user_wait', target:{repository:'a/b',commit:'a'.repeat(40),kind:'repository',number:0}, messages:[],sources:[],limitations:[],summary:'',updatedAt:'',expiresAt:'2026-12-01T00:00:00Z',metrics:{inputTokens:10,outputTokens:5,maxInputBytes:0,fetchedBytes:0,toolCalls:0,cacheHits:0,durationMs:0,heapPeakBytes:0} });
const render = (session:Session) => renderToString(createSSRApp(SessionContextPanel,{session,connected:true,provider:'openai',reviewPolicy:{maxFiles:10,diffTokens:20000,inputTokens:30000,inputBytes:262144,maxCalls:100,maxMinutes:15,runInputTokens:500000,runOutputTokens:64000},deleteId:''}));
describe('phase usage and budgets',()=>{
 it('renders saved phases, retries and the persistent limit',async()=>{
  const s=fixture();s.tokenBudget=100;s.metrics.phases={edit:{calls:2,inputTokens:7,outputTokens:3,durationMs:1234},edit_retry:{calls:1,inputTokens:3,outputTokens:2,durationMs:500}};
  const html=await render(s);
  expect(html).toContain('編集の再試行');expect(html).toContain('入力7 / 出力3');expect(html).toContain('value="100"');expect(html).toContain('金額の上限ではありません');
 });
 it('does not invent phase usage for old sessions',async()=>{
  const html=await render(fixture());expect(html).not.toContain('段階別の利用量');expect(html).toContain('value="0"');
 });
 it.each([10,15])('warns and disables saving a budget of %i at or below cumulative usage',async(tokenBudget)=>{
  const s=fixture();s.tokenBudget=tokenBudget;
  const html=await render(s);
  expect(html).toContain('現在の累計（15トークン）を超える値にしてください');
  expect(html).toMatch(/<button[^>]*disabled[^>]*>上限を保存<\/button>/);
 });
 it.each([0,16])('allows a budget of %i that is unlimited or above cumulative usage',async(tokenBudget)=>{
  const s=fixture();s.tokenBudget=tokenBudget;
  const html=await render(s);
  expect(html).not.toContain('role="alert"');
  expect(html).toMatch(/<button class="plain-button">上限を保存<\/button>/);
 });
});
