<script setup lang="ts">
import { computed, onUnmounted, ref, watch } from 'vue';
import { api } from '../lib/auth';
import { lineDiff, type DiffLine } from '../lib/lineDiff';
import { implementationInterrupted, implementationStatus } from '../lib/implementationStatus';
import type { Implementation } from '../types';

const props = defineProps<{ sessionId: string; repository: string; implementation: Implementation; sessionState: string; running: boolean; canRun: boolean; connected: boolean }>();
const emit = defineEmits<{ approve: []; resume: []; checkCi: []; fixCi: []; verify: []; amend: [boolean]; publish: [{ branch: string; title: string; body: string }] }>();
const impl = computed(() => props.implementation);
const labels = { create: '追加', update: '変更', delete: '削除' } as const;
const interrupted = computed(() => implementationInterrupted(impl.value, props.sessionState));
const statusLabel = computed(() => implementationStatus(impl.value, props.sessionState));
const branch = ref(''); const title = ref(''); const body = ref(''); const confirmed = ref(false);
watch(() => [impl.value.status, impl.value.version, impl.value.branch, impl.value.title] as const, () => {
 branch.value = impl.value.branch || ''; title.value = impl.value.title || ''; body.value = (impl.value.body || '').slice(0, 12000); confirmed.value = false;
}, { immediate: true });
const followUp = computed(() => !!impl.value.expectedHead);
const branchValid = computed(() => /^ai-factory\/[a-z0-9][a-z0-9._-]{0,62}(\/[a-z0-9][a-z0-9._-]{0,62})?$/.test(branch.value) && !branch.value.includes('..') && !branch.value.endsWith('.lock'));
const verificationComplete = computed(() => !!impl.value.plan?.acceptanceCriteria?.length && !!impl.value.verification && !impl.value.verification.issues.length && !impl.value.verification.unverified.length && impl.value.plan.acceptanceCriteria.every(c => impl.value.verification?.criteria?.some(r => r.id===c.id && r.status==='met' && !!r.evidence.trim() && !!r.paths.length)));
const canPublish = computed(() => (impl.value.status==='publishing' || verificationComplete.value) && props.connected && !props.running && confirmed.value && branchValid.value && !!title.value.trim() && title.value.length <= 256 && new TextEncoder().encode(body.value).length <= 12000 && (impl.value.status === 'ready' || impl.value.status === 'publishing'));
const progress = computed(() => impl.value.steps?.length ? `${Math.min(impl.value.nextStep, impl.value.steps.length)} / ${impl.value.steps.length} 区切り` : '');

type Change = { original: string; content: string; lines: DiffLine[] };
const selected = ref(''); const diffs = ref<Record<string, Change>>({}); const pending = ref(''); const error = ref('');
const controller = new AbortController(); let active = true;
watch(() => [impl.value.version, impl.value.changeRevision] as const, () => { diffs.value = {}; selected.value = ''; pending.value = ''; });
async function open(path: string) {
 if (selected.value === path) { selected.value = ''; return; }
 selected.value = path; error.value = '';
 if (diffs.value[path]) return;
 const version = impl.value.version; const revision = impl.value.changeRevision;
 pending.value = path;
 try {
  const change = await api<{ original: string; content: string }>(`/sessions/${encodeURIComponent(props.sessionId)}/change?path=${encodeURIComponent(path)}`, { signal: controller.signal });
  if (!active || impl.value.version !== version || impl.value.changeRevision !== revision) return;
  diffs.value[path] = { ...change, lines: lineDiff(change.original, change.content) };
 } catch (e) {
  if (active && impl.value.version === version && impl.value.changeRevision === revision && selected.value === path) error.value = e instanceof Error && e.message === 'implementation_changes_expired' ? '変更内容の保存期限（1時間）が切れました。チャットでもう一度実装を依頼してください。' : '差分を取得できませんでした。権限や接続を確認してください。';
 } finally { if (active && impl.value.version === version && impl.value.changeRevision === revision && pending.value === path) pending.value = ''; }
}
function safeLink(url?: string) { try { const u = new URL(url || ''); return u.protocol === 'https:' && u.hostname === 'github.com' ? u.href : '#'; } catch { return '#'; } }
onUnmounted(() => { active = false; controller.abort(); });
</script>

<template>
 <section class="implementation" aria-label="実装とPR作成">
  <header><strong>実装とPR作成</strong><span class="impl-status">{{ statusLabel }}<template v-if="impl.status === 'editing' && progress"> · {{ progress }}</template></span></header>
  <div v-if="interrupted" class="impl-amend" role="status">
   <p>処理は中断しています。保存済みの計画や変更を引き継いで再開できます。</p>
   <p v-if="!connected">再接続が完了してから再開してください。</p>
   <p v-else-if="!canRun && impl.status!=='checking_ci'">画面上部でAPIキーを接続してから再開してください。</p>
   <button class="primary-button" :disabled="running || !connected || (!canRun && impl.status!=='checking_ci')" @click="emit('resume')">保存済みの状態から再開</button>
  </div>
  <p class="impl-request">{{ impl.request }}</p>
  <p v-if="impl.baseCommit" class="impl-base">ベース <code>{{ impl.baseBranch }}</code> <code>{{ impl.baseCommit.slice(0, 12) }}</code><template v-if="followUp"> · 作成済みのブランチ <code>{{ impl.branch }}</code> に追加コミット</template></p>

  <p v-if="impl.status === 'plan_ready' && (!impl.plan || impl.clarification)" class="impl-question" role="status">計画を作る前に確認したいことがあります。チャットの最後のメッセージの質問に、メッセージの種類「実装を依頼」で答えると計画を作り直します。</p>
  <p v-if="impl.revising">作成済みの変更を引き継いで追加修正しています。</p>
  <p v-if="impl.repairRound">整合性確認に基づく修正: {{ impl.repairRound }} / 2 回</p>
  <template v-if="impl.plan">
   <h3>実装計画</h3>
   <details v-if="impl.plan.acceptanceCriteria?.length" open><summary>受け入れ条件</summary><ol><li v-for="c in impl.plan.acceptanceCriteria" :key="c.id"><strong>{{ c.id }}: {{ c.requirement }}</strong><p>確認するコード: {{ c.paths.join('、') }}</p><p v-for="check in c.checks" :key="check">{{ check }}</p><p v-if="c.failureRefs?.length">対応するCI失敗: {{ c.failureRefs.join('、') }}</p></li></ol></details>
   <p class="impl-summary">{{ impl.plan.summary }}</p>
   <ul class="impl-files"><li v-for="file in impl.plan.files" :key="file.path"><code>{{ file.path }}</code><span class="impl-action" :class="file.action">{{ labels[file.action] }}</span><p>{{ file.approach }}</p><p v-if="file.dependsOn?.length">依存する変更: {{ file.dependsOn.join('、') }}</p></li></ul>
   <details v-if="impl.steps?.length"><summary>実装順（{{ impl.steps.length }} 区切り）</summary><ol><li v-for="(step, i) in impl.steps" :key="i">{{ step.files.join('、') }}</li></ol><p>相互に依存するファイルは同じ区切りで変更します。</p></details>
   <details v-if="impl.plan.outOfScope?.length || impl.plan.risks?.length || impl.plan.tests?.length"><summary>対象外・リスク・確認すべきテスト</summary>
    <p v-for="v in impl.plan.outOfScope || []" :key="'o' + v">対象外: {{ v }}</p><p v-for="v in impl.plan.risks || []" :key="'r' + v">リスク: {{ v }}</p><p v-for="v in impl.plan.tests || []" :key="'t' + v">テスト: {{ v }}</p>
   </details>
   <div v-if="impl.status === 'plan_ready' && !impl.clarification" class="impl-actions">
    <button class="primary-button" :disabled="running || !canRun" @click="emit('approve')">計画を承認して変更を作成</button>
    <span>計画を直したい場合は、チャットで追加の依頼を送ってください。計画を作り直します。</span>
   </div>
  </template>

  <section v-if="impl.status === 'amend_ready' && impl.pendingFiles?.length" class="impl-amend" aria-label="計画にないファイルの追加">
   <h3>計画にないファイルの追加</h3>
   <p>作業の途中で、次のファイルも変更が必要だと分かりました。承認すると計画に加えて作業を続けます。見送ると、これらは変更せずに残りの作業を続けます。</p>
   <ul class="impl-files"><li v-for="file in impl.pendingFiles" :key="file.path"><code>{{ file.path }}</code><span class="impl-action" :class="file.action">{{ labels[file.action] }}</span><p>{{ file.approach }}</p><p v-if="file.dependsOn?.length">依存する変更: {{ file.dependsOn.join('、') }}</p><p class="impl-reason">理由: {{ file.reason }}</p></li></ul>
   <div class="impl-actions">
    <button class="primary-button" :disabled="running || !canRun" @click="emit('amend', true)">追加して続ける</button>
    <button class="plain-button" :disabled="running || !canRun" @click="emit('amend', false)">追加せずに続ける</button>
   </div>
  </section>

  <template v-if="impl.changes.length">
   <h3>変更内容 {{ impl.changes.length }} ファイル</h3>
   <div v-for="change in impl.changes" :key="change.path" class="diff-file">
    <button class="diff-toggle" type="button" :aria-expanded="selected === change.path" @click="open(change.path)"><span>{{ change.path }}</span><small>{{ labels[change.action] }} · {{ selected === change.path ? '閉じる' : '差分を見る' }}</small></button>
    <template v-if="selected === change.path">
     <p v-if="pending === change.path" role="status">差分を取得しています…</p>
     <p v-else-if="error" role="alert">{{ error }}</p>
     <div v-else-if="diffs[change.path]" class="patch-scroll" tabindex="0" role="region" :aria-label="`${change.path} の差分`"><pre><span v-for="(line, i) in diffs[change.path].lines" :key="i" :class="line.kind"><i>{{ line.kind === 'add' ? '+' : line.kind === 'remove' ? '-' : ' ' }}</i>{{ line.text }}</span></pre></div>
    </template>
   </div>

  </template>

  <details v-if="impl.unverified.length" open><summary>未確認事項 {{ impl.unverified.length }} 件</summary><ul><li v-for="(u, i) in impl.unverified" :key="i">{{ u }}</li></ul></details>

  <section v-if="impl.verification" aria-label="AIによる整合性確認">
   <h3>AIによる整合性確認</h3>
   <p v-if="!impl.verification.issues.length && !impl.verification.unverified.length && impl.status!=='verification_blocked'">確認した範囲で不整合の指摘はありません。</p>
   <ul v-else><li v-for="(issue, i) in impl.verification.issues" :key="i">{{ issue }}</li></ul>
   <details v-if="impl.verification.unverified.length" open><summary>確認できなかった点</summary><ul><li v-for="(item, i) in impl.verification.unverified" :key="i">{{ item }}</li></ul></details>
   <ul v-if="impl.verification.criteria?.length"><li v-for="c in impl.verification.criteria" :key="c.id">{{ c.id }}: {{ { met: 'コードで確認', unmet: '未達', unverified: '未確認' }[c.status] }} — {{ c.evidence }}</li></ul>
   <p>コードを読み直した結果です。実行結果はGitHub CIの欄で確認できます。</p>
   <div v-if="impl.status==='verification_blocked'" class="impl-amend" role="status"><p>要求の達成を確認できていないため公開を止めています。チャットで追加修正を依頼するか、関連コードを再調査してください。</p><button class="plain-button" :disabled="running || !canRun" @click="emit('verify')">未確認事項を再調査</button></div>
  </section>

  <form v-if="impl.status === 'ready' || impl.status === 'publishing'" class="impl-publish" @submit.prevent="canPublish && emit('publish', { branch, title: title.trim(), body })">
   <h3>{{ followUp ? '作成済みのPRに追加コミット' : 'Draft PRを作成' }}</h3>
   <label>ブランチ名<input v-model="branch" :readonly="followUp" maxlength="140" spellcheck="false" /></label>
   <p v-if="!branchValid" role="alert">ブランチ名は <code>ai-factory/</code> で始まる英小文字・数字・記号（. _ -）にしてください。既存のブランチには書き込めません。</p>
   <label>{{ followUp ? 'コミットメッセージ' : 'PRタイトル' }}<input v-model="title" maxlength="256" /></label>
   <label v-if="!followUp">PR本文<textarea v-model="body" rows="8" maxlength="12000" /></label>
   <label class="consent"><input v-model="confirmed" type="checkbox" />差分を確認しました。{{ repository }} に新しいブランチを作成し、{{ followUp ? '追加コミットします' : 'Draft PRを作成します' }}。</label>
   <p>アプリ内ではコードやテストを実行していません。作成後は対象コミットのGitHub CIを最大5分待ち、結果を表示します。未完了ならCIを再確認できます。完了したGitHubへの書き込みは自動では取り消されません。</p>
   <button class="primary-button" :disabled="!canPublish">{{ impl.status === 'publishing' ? 'PRの作成を再試行' : followUp ? '追加コミットを作成' : 'Draft PRを作成' }}</button>
  </form>

  <section v-if="impl.ci" aria-label="GitHub CIの実行結果">
   <h3>GitHub CI</h3><p>対象コミット: <code>{{ impl.ci.commit.slice(0,12) }}</code> · {{ { pending: '未完了・チェック待ち', passed: '取得したチェックはすべて成功', failed: '失敗・修正が必要', unverified: '成功を確認できていません' }[impl.ci.state] }}</p>
   <ul><li v-for="(check,i) in impl.ci.checks" :key="i">{{ check.name }}: {{ check.conclusion || check.status }}</li></ul><p v-for="item in impl.ci.unverified" :key="item">{{ item }}</p>
   <div v-if="impl.status==='published'" class="impl-actions"><button class="plain-button" :disabled="running || !connected" @click="emit('checkCi')">CIを再確認</button><button v-if="impl.ci.state==='failed'" class="primary-button" :disabled="running || !canRun" @click="emit('fixCi')">CIの失敗を修正</button></div>
  </section>
  <p v-if="(impl.status === 'published' || impl.status === 'checking_ci') && impl.pullUrl" class="impl-done">作成済み: <a :href="safeLink(impl.pullUrl)" target="_blank" rel="noopener noreferrer">PR #{{ impl.pullNumber }}</a> · ブランチ <code>{{ impl.branch }}</code>。追加の修正はチャットで依頼すると、このブランチに追加コミットできます。</p>
 </section>
</template>

<style scoped>
.implementation{margin:18px 0;padding:16px 18px;border:1px solid #d5e2d9;border-radius:10px;background:#fbfdfb}
.implementation h3{margin:18px 0 6px;font-size:14px}.implementation header{display:flex;justify-content:space-between;gap:12px;align-items:center}.impl-status{font-size:12px;color:#315e31;background:#e8f3ea;border-radius:999px;padding:2px 10px}
.impl-request{white-space:pre-wrap;color:#33443a}.impl-base,.implementation details p,.impl-publish p{font-size:12px;color:#63766b}.impl-summary{white-space:pre-wrap}
.impl-files{list-style:none;padding:0}.impl-files li{padding:8px 0;border-top:1px solid #e2e8e4}.impl-files code,.diff-toggle span{overflow-wrap:anywhere;font-family:ui-monospace,SFMono-Regular,Consolas,monospace}.impl-files p{margin:4px 0 0;font-size:13px}
.impl-action{margin-left:8px;font-size:11px;border-radius:4px;padding:1px 6px;background:#eef2f0}.impl-action.create{background:#dafbe1;color:#116329}.impl-action.delete{background:#ffebe9;color:#82071e}
.impl-actions{display:flex;flex-wrap:wrap;gap:12px;align-items:center}.impl-actions span{font-size:12px;color:#63766b}
.diff-file{border-top:1px solid #e2e8e4}.diff-toggle{display:flex;justify-content:space-between;gap:12px;width:100%;padding:11px 0;text-align:left;border:0;background:none;color:#315e31;cursor:pointer}.diff-toggle small{flex-shrink:0;color:#63766b}
.patch-scroll{max-height:420px;overflow:auto;border:1px solid #d1d9e0;border-radius:6px;background:#f6f8fa}.patch-scroll pre{margin:0;min-width:max-content;font:12px/1.7 ui-monospace,SFMono-Regular,Consolas,monospace;tab-size:4}.patch-scroll span{display:block;padding:0 12px;white-space:pre;min-height:1.7em}.patch-scroll i{font-style:normal;display:inline-block;width:1.4em;color:#8a9a90}.patch-scroll .add{background:#dafbe1;color:#116329}.patch-scroll .remove{background:#ffebe9;color:#82071e}.patch-scroll .skip{background:#ddf4ff;color:#0550ae}
.impl-publish{display:grid;gap:10px;margin-top:14px}.impl-publish label{display:grid;gap:4px;font-size:13px}.impl-publish input,.impl-publish textarea{font:inherit;padding:8px;border:1px solid #cdd8d1;border-radius:6px}.impl-publish .consent{display:flex;gap:8px;align-items:flex-start}
.impl-question{padding:10px;background:#fff8e6;border-radius:6px;font-size:13px}.impl-done{padding:10px;background:#e8f3ea;border-radius:6px}.impl-amend{margin-top:12px;padding:10px;background:#fff8e6;border-radius:6px}.impl-amend>p{font-size:13px}.impl-reason{color:#7a6a3c}
</style>
