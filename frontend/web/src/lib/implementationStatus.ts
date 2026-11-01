import type { Implementation } from '../types';

export function implementationInterrupted(impl: Implementation, sessionState: string) {
 return ['planning', 'editing', 'verifying', 'checking_ci'].includes(impl.status) && ['failed', 'auth_wait', 'stopped', 'user_wait'].includes(sessionState);
}

export function implementationStatus(impl: Implementation, sessionState: string) {
 if (implementationInterrupted(impl, sessionState)) {
  return { planning: '計画作成を中断', editing: '変更作成を中断', verifying: '整合性確認を中断', checking_ci: 'CI確認を中断' }[impl.status as 'planning' | 'editing' | 'verifying' | 'checking_ci'];
 }
 if (impl.status === 'plan_ready' && (!impl.plan || impl.clarification)) return '依頼内容の確認待ち';
 if (impl.status==='published' && impl.ci) return { pending: 'PR作成済み・CI待ち', passed: 'CI成功', failed: 'CI失敗・修正が必要', unverified: 'PR作成済み・CI未確認' }[impl.ci.state];
 return { planning: '計画を作成中', plan_ready: '計画の承認待ち', editing: '変更を作成中', verifying: '変更全体の整合性を確認中', verification_blocked: '要求の達成を確認できず公開停止', amend_ready: '追加ファイルの承認待ち', ready: 'PR作成の確認待ち', publishing: 'PRを作成中', checking_ci: 'GitHub CIを確認中', published: 'PR作成済み' }[impl.status];
}
