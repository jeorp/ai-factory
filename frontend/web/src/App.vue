<script setup lang="ts">
import { computed, defineAsyncComponent, nextTick, onMounted, onUnmounted, ref, watch } from 'vue';
import { api, auth, loginComplete, logout } from './lib/auth';
import { clearKey, credentials, deleteSavedKey, savedKey, saveKey, setKey } from './lib/keyVault';
import { estimateCost, formatCost, pricing } from './lib/pricing';
import { useWorkspace } from './stores/workspace';
import { busy, states, type Config, type Repository, type Finding } from './types';
import Icon from './components/Icon.vue';
import SourceFiles from './components/SourceFiles.vue';
import ReviewDiffs from './components/ReviewDiffs.vue';
import FindingEvidence from './components/FindingEvidence.vue';
import FindingIssue from './components/FindingIssue.vue';
import { clearFiles } from './lib/fileCache';
import { fileKey, type SourceFile } from './lib/sourceFiles';

const CodeViewer = defineAsyncComponent(() => import('./components/CodeViewer.vue'));

const work = useWorkspace();
const config = ref<Config>({ demo: false, aiProvider: 'openai', models: [], installUrl: '' });
const login = ref(''); const loading = ref(true); const actionPending = ref(false); const appError = ref('');
const repositories = ref<Repository[]>([]); const repositoryChoice = ref(''); const manualTarget = ref(''); const target = computed(() => repositoryChoice.value === '__manual__' ? manualTarget.value.trim() : repositoryChoice.value); const model = ref(''); const text = ref('');
const keyDraft = ref(''); const keyChoice = ref<'saved' | 'new'>('new'); const keyPending = ref(false); const rememberKey = ref(false); const hasSavedKey = ref(false); const keyValid = ref(false); const consent = ref(false); const mode = ref('message');
const showNew = ref(false); const showDetails = ref(window.matchMedia('(min-width: 1100px)').matches); const deleteID = ref(''); const transcript = ref<HTMLElement>();
const openedFile = ref<SourceFile>(); const codePanel = ref<HTMLElement>(); let fileOpener: HTMLElement | undefined;
async function openFile(file: SourceFile, opener?: HTMLElement) {
 fileOpener = opener;
 openedFile.value = file;
 await nextTick(); codePanel.value?.focus({ preventScroll: true });
}
async function closeFile() {
 openedFile.value = undefined;
 await nextTick(); if (fileOpener?.isConnected) fileOpener.focus({ preventScroll: true });
 fileOpener = undefined;
}
function escapeFile(event: KeyboardEvent) { if (event.key === 'Escape' && openedFile.value) { event.preventDefault(); void closeFile(); } }
const current = computed(() => work.current); const running = computed(() => busy(current.value));
const reviewPolicy = computed(() => config.value.reviewPolicy || { maxFiles: 10, diffTokens: 20000, inputTokens: 30000, inputBytes: 262144, maxCalls: 100, maxMinutes: 15, runInputTokens: 500000, runOutputTokens: 64000 });
const inputLimitBytes = computed(() => current.value?.runKind === 'review' ? reviewPolicy.value.inputBytes : 24576);
const priorityLabels = { high: '高', medium: '中', low: '低', unknown: '未評価' } as const;
const unverifiedItems = computed(() => {
 const review = current.value?.review;
 if (!review) return [];
 const order = { high: 0, medium: 1, low: 2, unknown: 3 };
 return review.unverified.map((text, index) => ({ text, index, priority: review.priorities?.[index] }))
  .filter(item => !!item.priority || review.phase === 'done')
  .sort((a, b) => (order[a.priority?.level || 'unknown'] ?? 3) - (order[b.priority?.level || 'unknown'] ?? 3) || a.index - b.index);
});
const sessionCost = computed(() => current.value ? estimateCost(config.value.aiProvider, current.value.model, current.value.metrics.inputTokens || 0, current.value.metrics.outputTokens || 0) : undefined);
const sessionPricing = computed(() => current.value ? pricing(config.value.aiProvider, current.value.model) : undefined);
const canSend = computed(() => !!text.value.trim() && work.connected && (mode.value === 'instruction' || (!running.value && (config.value.aiProvider === 'demo' || keyValid.value))));
const hints = ['このリポジトリの構成を教えて', 'READMEからセットアップ手順を整理して', 'PR一覧を見せて', '取得したPRをレビューして'];
const errors: Record<string, string> = {
 review_read_not_allowed: 'AIがレビュー中に利用できない情報の取得を繰り返したため停止しました。保存済みの状態から再開してください。',
 pr_changed_create_new_review: 'PRのベースまたはヘッドが更新されました。チャットで「もう一度レビューして」と依頼してください。', pull_request_required: 'チャットでレビューするPRの番号かURLを教えてください。',
 login_required: 'ログインの有効期限が切れました。GitHubでログインし直してください。', github_login_required: 'GitHubでログインしてください。',
 repository_forbidden: 'このリポジトリを参照できません。GitHub Appのアクセス許可を確認してください。',
 session_busy: '別の接続でセッションを使用中です。接続を閉じて、最大45秒ほど待ってください。', server_busy: '接続数の上限に達しています。少し待ってから再接続してください。',
 ai_key_required: 'AIのAPIキーを入力してください。有効期間は入力から最大1時間です。',
 connection_failed: '接続できませんでした。APIの起動状態を確認してください。', connection_required: '再接続してから送信してください。',
 invalid_message_no_secrets: '本文は4,096バイト以内にし、APIキーなどの認証情報を含めないでください。',
 invalid_instruction: '追加指示は4,096バイト以内にし、認証情報を含めないでください。', instruction_limit: '追加指示の合計上限に達しました。新しいセッションを作成してください。',
 history_limit_create_new_session: '会話の保存上限に達しました。新しいセッションを作成してください。',
 invalid_repository: 'owner/repository またはGitHubのリポジトリ・PR・Issue URLを指定してください。', invalid_github_url: 'GitHub.comのリポジトリ・PR・Issue URLを指定してください。',
 store_unavailable: '保存先に接続できません。Firebase EmulatorまたはFirestoreを確認してください。', openai_http_401: 'OpenAI APIキーを確認してください。', openai_http_429: 'OpenAIの利用枠またはレート制限に達しました。', gemini_key_invalid: 'Gemini APIキーが無効と判定されました。Google AI Studioでキーを確認してください。', gemini_api_disabled: 'Gemini APIの利用設定またはキーの制限を確認してください。', gemini_bad_request: 'Geminiへのリクエスト形式が拒否されました。', gemini_precondition: 'Geminiプロジェクトの利用条件を確認してください。', gemini_http_400: 'Geminiがリクエストを拒否しました。キーやモデルの設定を確認してください。', gemini_http_404: 'このプロジェクトでモデルを利用できません。新しいセッションで利用可能なモデルを選択してください。', gemini_http_403: 'Gemini APIキーやプロジェクトの設定を確認してください。', gemini_http_429: 'Geminiの利用枠またはレート制限に達しました。',
 gemini_output_limit: 'Geminiの応答を処理できませんでした。サーバーを最新版に更新して再試行してください。',
 gemini_answer_too_long: 'Geminiの回答または要約がアプリの長さ制限を超えました。質問の範囲を絞って再試行してください。',
 gemini_incomplete: 'Geminiの応答が途中で終了しました。再試行してください。',
 gemini_invalid_intent: 'Geminiが質問の意図や調査方針を適切な形式で返せませんでした。再試行してください。',
 gemini_invalid_review: 'Geminiがレビュー結果を適切な形式で返せませんでした。再試行してください。',
 gemini_invalid_review_action: 'Geminiがレビュー開始の指示を適切な形式で返せませんでした。再試行してください。',
};
function errorMessage(value: unknown) { const code = value instanceof Error ? value.message : String(value); return errors[code] || `処理を完了できませんでした（${code}）。`; }
async function action(fn: () => Promise<void>) { if (actionPending.value) return; actionPending.value = true; appError.value = ''; try { await fn(); } catch (e) { appError.value = errorMessage(e); } finally { actionPending.value = false; } }
async function loadWorkspace() { const me = await api<{ login: string }>('/me'); login.value = me.login; await work.refresh(); await refreshRepositories(); }
async function refreshRepositories() { repositories.value = await api<Repository[]>('/repositories'); if (repositoryChoice.value && repositoryChoice.value !== '__manual__' && !repositories.value.some(r => r.full_name === repositoryChoice.value)) repositoryChoice.value = ''; }
async function signIn() { await action(async () => { if (config.value.demo) { await loginComplete(); await loadWorkspace(); } else { location.assign('/auth/github'); } }); }
async function signOut() { await action(async () => { const owner = auth.currentUser?.uid; work.disconnect(true); keyDraft.value = ''; clearKey(); await nextTick(); if (owner) await clearFiles(owner).catch(() => {}); await logout(); login.value = ''; work.sessions = []; repositories.value = []; repositoryChoice.value = ''; manualTarget.value = ''; }); }
async function createSession() { await action(async () => { const s = await api<NonNullable<typeof current.value>>('/sessions', { method: 'POST', body: JSON.stringify({ target: target.value, model: model.value }) }); await work.open(s); showNew.value = false; repositoryChoice.value = ''; manualTarget.value = ''; consent.value = false; keyDraft.value = ''; syncKey(); }); }
async function applyKey() {
 if (keyPending.value) return;
 appError.value = ''; keyPending.value = true;
 const sessionID = current.value?.id;
 try {
  const key = keyChoice.value === 'saved' ? await savedKey(config.value.aiProvider) : keyDraft.value.trim();
  if (sessionID !== current.value?.id) return;
  if (!consent.value || !key || key.length > 512 || /\s/.test(key)) { appError.value = '送信内容を確認し、AIのAPIキーを選択または入力してください。'; return; }
  if (keyChoice.value === 'new' && rememberKey.value) { await saveKey(config.value.aiProvider, key); hasSavedKey.value = true; }
  if (sessionID !== current.value?.id) return;
  setKey(key); keyDraft.value = ''; syncKey();
 } catch { appError.value = 'APIキーの保存または読み込みに失敗しました。ブラウザの保存設定を確認してください。'; }
 finally { keyPending.value = false; }
}
async function refreshSavedKey() { try { hasSavedKey.value = !!(await savedKey(config.value.aiProvider)); keyChoice.value = hasSavedKey.value ? 'saved' : 'new'; } catch { appError.value = '保存済みのAPIキーを読み込めませんでした。ブラウザの保存設定を確認してください。'; } }
async function forgetSavedKey() { try { await deleteSavedKey(config.value.aiProvider); hasSavedKey.value = false; keyChoice.value = 'new'; keyDraft.value = ''; rememberKey.value = false; clearKey(); syncKey(); } catch { appError.value = '保存済みのAPIキーを削除できませんでした。'; } }
function syncKey() { const c = credentials(); keyValid.value = !!c.apiKey; }
function send() { if (!canSend.value) return; try { work.send(mode.value, text.value.trim()); text.value = ''; } catch (e) { appError.value = errorMessage(e); } }
function investigate(index: number) { try { work.send('investigate', '', crypto.randomUUID(), index); } catch (e) { appError.value = errorMessage(e); } }
function resume() { try { work.send('resume'); } catch (e) { appError.value = errorMessage(e); } }
function stop() { try { work.send('stop'); } catch (e) { appError.value = errorMessage(e); } }
function end() { try { if (work.connected) work.send('end'); } finally { work.disconnect(true); keyDraft.value = ''; syncKey(); } }
function selectHint(hint: string) { text.value = hint; }
function keydown(event: KeyboardEvent) { if (event.key === 'Enter' && !event.shiftKey && !event.isComposing) { event.preventDefault(); send(); } }
function safeLink(url: string) { try { const u = new URL(url); return u.protocol === 'https:' && u.hostname === 'github.com' ? u.href : '#'; } catch { return '#'; } }
function findingLink(finding: Finding) {
 const review = current.value?.review;
 if (!review || !current.value) return '#';
 const file = review.steps.flatMap(step => step.files).find(file => file.path === finding.path);
 const sourcePath = finding.side === 'LEFT' ? file?.previousPath || finding.path : finding.path;
 return safeLink(`https://github.com/${current.value.target.repository}/blob/${finding.side === 'LEFT' ? review.base : review.head}/${sourcePath.split('/').map(encodeURIComponent).join('/')}${finding.line ? '#L' + finding.line : ''}`);
}
function date(value: string) { return new Intl.DateTimeFormat('ja-JP', { month: 'short', day: 'numeric' }).format(new Date(value)); }
let keyTimer: ReturnType<typeof setInterval>;
const pagehide = () => { clearKey(); work.disconnect(true); };
onMounted(async () => {
 keyTimer = setInterval(syncKey, 1000); window.addEventListener('pagehide', pagehide); window.addEventListener('keydown', escapeFile);
 try { const response = await fetch('/api/config'); if (!response.ok) throw new Error('APIの設定を取得できません'); config.value = await response.json(); await refreshSavedKey(); model.value = config.value.models[0] || ''; await auth.authStateReady();
  if (new URLSearchParams(location.search).has('login')) { history.replaceState({}, '', '/'); await loginComplete(); }
  if (auth.currentUser) await loadWorkspace();
 } catch (e) { appError.value = errorMessage(e); } finally { loading.value = false; }
});
onUnmounted(() => { clearInterval(keyTimer); window.removeEventListener('pagehide', pagehide); window.removeEventListener('keydown', escapeFile); clearKey(); work.disconnect(true); });
watch(() => current.value?.messages.length, async () => { await nextTick(); transcript.value?.scrollTo({ top: transcript.value.scrollHeight, behavior: 'smooth' }); });
watch(() => [current.value?.id, current.value?.state] as const, ([id, state], [previousId, previous]) => { if (id === previousId && state === 'failed' && previous && ['running', 'accepted', 'stopping'].includes(previous) && ['gemini_key_invalid', 'openai_http_401'].includes(current.value?.limitations?.at(-1) || '')) { clearKey(); syncKey(); appError.value = 'APIキーが無効です。保存済みのキーを削除するか、新しいキーを入力してください。'; } });
watch(() => current.value?.id, () => { keyDraft.value = ''; text.value = ''; consent.value = false; rememberKey.value = false; void refreshSavedKey(); syncKey(); });
watch([() => current.value?.id, showNew], () => { openedFile.value = undefined; fileOpener = undefined; });
</script>

<template>
 <div class="app-shell">
  <aside class="sidebar">
   <a class="brand" href="/" aria-label="AI Factory ホーム"><span class="brand-symbol"><Icon name="grid" :size="19" /></span><span>AI Factory<span class="brand-dot">.</span></span></a>
   <div class="workspace-label">PERSONAL WORKSPACE <span class="tiny-tag">MVP</span></div>
   <button class="new-chat" :disabled="!login" aria-label="新しいセッション" @click="showNew = true"><Icon name="plus" :size="17" />新しいセッション</button>
   <div class="nav-active"><Icon name="chat" :size="17" />GitHub チャット<span class="count">{{ work.sessions.length }}</span></div>
   <div class="section-caption">最近のセッション</div>
   <div class="session-list">
    <p v-if="!work.sessions.length" class="empty-sidebar">セッションを作成すると、<br />ここから会話に戻れます。</p>
    <div v-for="s in work.sessions" :key="s.id" class="session-item" :class="{ selected: current?.id === s.id }">
     <button class="session-select" :aria-label="`${s.target.repository}の会話（${date(s.updatedAt)}）`" @click="action(async () => { await work.open(s); showNew = false; })"><Icon name="chat" :size="15" /><span><strong>{{ s.target.repository }}</strong><small>{{ date(s.updatedAt) }} · {{ states[s.state] || s.state }}</small></span></button>
     <button class="icon-button delete-button" :aria-label="`${s.title}を削除`" @click="deleteID = deleteID === s.id ? '' : s.id"><Icon name="trash" :size="14" /></button>
     <div v-if="deleteID === s.id" class="delete-confirm">会話と結果を削除します。<button @click="action(async () => { await work.remove(s.id); deleteID = ''; })">削除する</button></div>
    </div>
   </div>
   <div class="sidebar-bottom"><div class="readonly-note"><Icon name="shield" :size="16" /><span>GitHubへのアクセスは<br /><strong>読み取り専用</strong>です</span></div>
    <div class="profile"><span class="avatar">{{ login ? login[0]?.toUpperCase() : 'G' }}</span><div><strong>{{ login || 'ゲスト' }}</strong><small>{{ config.demo ? 'ローカルデモ' : 'Personal account' }}</small></div><button v-if="login" class="icon-button" aria-label="ログアウト" @click="signOut"><Icon name="logout" :size="17" /></button></div>
   </div>
  </aside>

  <main class="main-panel">
   <header class="topbar"><div class="breadcrumb">ワークスペース<Icon name="chevron" :size="13" /><strong>GitHub チャット</strong></div><div class="top-actions"><span class="badge" :class="{ demo: config.demo }"><span class="status-dot"></span>{{ config.demo ? 'デモモード' : 'READ ONLY' }}</span><button v-if="current" class="icon-button" aria-label="参照情報の表示を切り替え" @click="openedFile = undefined; showDetails = !showDetails"><Icon name="grid" :size="17" /></button></div></header>
   <div v-if="appError || work.error" class="error-banner" role="alert">{{ appError || errorMessage(work.error) }}<button class="plain-button" @click="appError = ''; work.error = ''">閉じる</button></div>
   <div v-if="config.demo" class="demo-banner">{{ config.aiProvider === 'demo' ? '固定デモです。実際のGitHubやAIには接続しません。' : 'GitHubは固定デモです。質問とサンプル情報はGeminiへ送信されます。' }}</div>

   <div v-if="!current || showNew" class="welcome-scroll">
    <div class="welcome">
     <div class="eyebrow"><span class="green-line"></span> YOUR REPOSITORY, IN CONTEXT</div>
     <h1>コードを読み解く、<br /><span>次の会話をはじめよう。</span></h1>
     <p class="welcome-description">リポジトリの構成も、PRの背景も、気になるIssueも。<br />GitHubの情報を必要な分だけ集めて、根拠とともに整理します。</p>
     <div class="setup-card">
      <div class="card-heading"><div class="number-label">01</div><div><h2>{{ login ? '調べたいリポジトリを選択' : 'GitHubとつなげて、はじめる' }}</h2><p>{{ login ? '許可したリポジトリ・PR・Issueを参照できます。' : 'GitHub Appで、参照するリポジトリを選べます。' }}</p></div><Icon name="branch" :size="22" /></div>
      <div v-if="!login" class="login-area"><button class="primary-button" :disabled="loading || actionPending" @click="signIn"><Icon name="code" :size="18" />{{ config.demo ? '開発用ログイン' : 'GitHubでログイン' }}<Icon name="chevron" :size="16" /></button><span>コードの変更や投稿は行いません。</span></div>
      <form v-else @submit.prevent="createSession">
       <label for="repository">リポジトリ</label>
       <select id="repository" v-model="repositoryChoice" class="repository-select" required><option value="" disabled>リポジトリを選択してください</option><option v-for="r in repositories" :key="r.full_name" :value="r.full_name">{{ r.full_name }}</option><option value="__manual__">リポジトリ・PR・Issueを直接入力</option></select>
       <div v-if="repositoryChoice === '__manual__'" class="manual-target"><label for="target">リポジトリ / PR / Issue</label><div class="input-with-icon"><Icon name="link" :size="17" /><input id="target" v-model="manualTarget" placeholder="owner/repository または https://github.com/…" required maxlength="1024" /></div></div>
       <div class="repository-actions"><span>{{ repositories.length }} 件のリポジトリに接続</span><button class="plain-button" type="button" @click="action(refreshRepositories)"><Icon name="refresh" :size="12" />再取得</button><a v-if="!config.demo" :href="config.installUrl" target="_blank" rel="noopener noreferrer">アクセスを設定<Icon name="external" :size="12" /></a></div>
       <div class="form-bottom"><div class="model-select"><label for="model">AIモデル</label><select id="model" v-model="model"><option v-for="m in config.models" :key="m">{{ m }}</option></select></div><button class="primary-button" :disabled="actionPending || !target"><span>セッションを作成</span><Icon name="chevron" :size="16" /></button></div>
      </form>
     </div>
     <div class="feature-grid"><div><Icon name="link" :size="20" /><h3>根拠から、わかる。</h3><p>参照元と未確認の範囲を<br />一緒に確認できます。</p></div><div><Icon name="grid" :size="20" /><h3>必要な情報だけ。</h3><p>会話と取得情報を分け、<br />コンテキストを小さく保ちます。</p></div><div><Icon name="key" :size="20" /><h3>キーの保存を選べる。</h3><p>自分のAI APIキーを使用。<br />ブラウザへの保存は任意です。</p></div></div>
     <div class="welcome-footer"><span class="small-orbit">✳</span> 小さく読み取り、理解を深める。<span>PHASE 02 — PR REVIEW</span></div>
    </div>
   </div>

   <div v-else class="conversation-layout" :class="{ 'details-hidden': !showDetails && !openedFile, 'code-open': !!openedFile }">
    <section class="conversation">
     <div class="conversation-header"><div><span class="section-caption">GITHUB SESSION</span><h2><Icon name="folder" :size="19" />{{ current.target.repository }}</h2></div><div><span class="session-state" :class="{ pulse: running }">{{ states[current.state] }}</span><button class="plain-button" @click="end">終了</button></div></div>
     <div v-if="config.aiProvider !== 'demo' && !keyValid" class="key-card">
      <div class="key-heading"><Icon name="key" :size="17" /><strong>{{ config.aiProvider === 'gemini' ? 'Gemini' : 'OpenAI' }} APIキーを接続</strong><span>利用は最大1時間</span></div>
      <p v-if="config.aiProvider === 'gemini'">回答の生成時に非公開コードを含む取得情報をGoogleへ送信します。無料枠では送信内容がGoogleのサービス改善に使われる場合があります。利用枠や料金はGoogleのプロジェクト設定によります。</p><p v-else>回答の生成時に、非公開コードを含む取得情報をOpenAIへ送信します。API利用料はご自身のOpenAIアカウントに発生します。ChatGPTの契約とは別です。</p>
      <label class="consent"><input v-model="consent" type="checkbox" />送信内容と費用について確認しました</label>
      <div v-if="hasSavedKey" role="group" aria-label="APIキーの選択">
       <label class="consent"><input v-model="keyChoice" type="radio" value="saved" name="key-choice" />保存済みのAPIキーを使う</label>
       <label class="consent"><input v-model="keyChoice" type="radio" value="new" name="key-choice" />新しいAPIキーを入力する</label>
       <button class="plain-button" type="button" @click="forgetSavedKey">保存済みのキーを削除</button>
      </div>
      <form @submit.prevent="applyKey">
       <template v-if="keyChoice === 'new'">
        <div class="key-form"><input v-model="keyDraft" type="password" autocomplete="off" spellcheck="false" placeholder="APIキーを入力" aria-label="AI APIキー" maxlength="512" /></div>
        <label class="consent"><input v-model="rememberKey" type="checkbox" />APIキーをこのブラウザに保存する{{ hasSavedKey ? '（保存済みのキーを上書き）' : '' }}</label>
        <p>保存したキーは次のセッションでも選択できます。共有端末では保存を避けてください。</p>
       </template>
       <button class="primary-button" :disabled="!consent || keyPending">接続する</button>
      </form>
     </div>
     <div ref="transcript" class="transcript" role="log" aria-label="会話履歴" aria-live="polite">
      <details v-if="current.review" :key="current.id" class="review-results">
       <summary>PRレビュー結果 · {{ current.review.title }} · 指摘 {{ current.review.findings.length }} 件</summary>
       <h2>{{ current.review.title }}</h2>
       <p class="review-commits">Base <code>{{ current.review.base.slice(0, 12) }}</code> → Head <code>{{ current.review.head.slice(0, 12) }}</code></p>
       <p>{{ { planning: '計画作成中', diff: '差分を確認中', cross: 'ファイル間の整合性を確認中', priority: '未確認項目の優先度を確認中', done: '確認完了' }[current.review.phase] || current.review.phase }} · {{ current.review.steps.reduce((n, s) => n + s.files.filter(f => f.done).length, 0) }} / {{ current.review.totalFiles }} ファイルの処理済み（取得失敗・対象外を含む）</p>
       <p v-if="current.review.scope === 'backend' || current.review.scope === 'frontend'">対象範囲: {{ current.review.scope === 'backend' ? 'バックエンド（frontend/ を除外）' : 'フロントエンド（backend/ を除外）' }}。共通の設定・ドキュメントも確認します。</p>
       <details><summary>レビュー計画と処理状況</summary><div v-for="(step, i) in current.review.steps" :key="i" class="review-step"><strong>{{ i + 1 }}. {{ step.directory }} · {{ { pending: '未処理', running: '確認中', done: '処理済み', partial: '未確認あり', excluded: '指定により対象外' }[step.state] || step.state }}</strong><ul><li v-for="file in step.files" :key="file.index">{{ file.path || `一覧取得失敗（${file.index + 1}番目）` }} · {{ file.excluded ? '指定により対象外（AI確認なし）' : file.done ? '処理済み' : file.offset ? '差分の途中まで処理済み' : '未処理' }}</li></ul><p v-if="step.summary">{{ step.summary }}</p></div></details>
       <ReviewDiffs :key="`${current.id}-${current.review.base}-${current.review.head}`" :session-id="current.id" :review="current.review" />
       <h3>指摘 {{ current.review.findings.length }} 件</h3>
       <p v-if="!current.review.findings.length">{{ current.review.phase === 'done' ? '確認した範囲では、保存された指摘はありません。' : '指摘は確認した区切りごとに表示されます。' }}</p>
       <article v-for="(finding, i) in current.review.findings" :key="i" class="review-finding">
        <h3><span class="finding-severity">{{ finding.severity }}</span> {{ finding.title }}</h3>
        <a :href="findingLink(finding)" target="_blank" rel="noopener noreferrer">{{ finding.path }}{{ finding.line ? ':' + finding.line : '（行未特定）' }} · {{ finding.side === 'LEFT' ? '変更前' : '変更後' }}</a>
        <p><strong>問題:</strong> {{ finding.problem }}</p><p><strong>影響:</strong> {{ finding.impact }}</p><FindingEvidence :evidence="finding.evidence" /><p v-if="finding.suggestion"><strong>修正案:</strong> {{ finding.suggestion }}</p>
        <FindingIssue v-if="!config.demo && current.target.kind === 'pull' && current.target.number" :finding="finding" :repository="current.target.repository" :pull-number="current.target.number" :source-url="findingLink(finding)" />
       </article>
       <details v-if="current.review.unverified.length" open><summary>未確認の範囲・制限 {{ current.review.unverified.length }} 件</summary>
        <p>優先度はレビューへの影響の推定です。未評価の項目も残します。追加調査では関連情報を再確認します。</p><p v-if="unverifiedItems.length < current.review.unverified.length" role="status">未確認項目の優先度を確認しています。中断した場合は保存済みの状態から再開できます。</p>
        <ul class="unverified-list"><li v-for="entry in unverifiedItems" :key="entry.index"><p><strong class="priority-label">優先度 {{ priorityLabels[entry.priority?.level || 'unknown'] }}</strong> {{ entry.text }}</p><p v-if="entry.priority?.reason" class="priority-reason">{{ entry.priority.reason }}</p><button type="button" class="investigate-button" :disabled="running || !work.connected || (config.aiProvider !== 'demo' && !keyValid)" @click="investigate(entry.index)">{{ current.review.investigations?.[entry.index] ? 'もう一度調査' : '追加調査' }}</button><span v-if="current.review.pendingInvestigation === entry.index + 1" class="investigation-status">{{ running ? '調査中' : '調査が中断されています。保存済みの状態から再開できます。' }}</span><div v-if="current.review.investigations?.[entry.index]" class="investigation-result"><strong>追加調査の結果</strong><p>{{ current.review.investigations[entry.index] }}</p></div></li></ul>
       </details>
      </details>
      <div v-if="!current.messages.length" class="chat-empty"><span class="assistant-mark"><Icon name="grid" :size="24" /></span><h2>何から読み解きましょう？</h2><p>PRを取得したあと、「このPRをレビューして」と続けて依頼できます。</p><div class="suggestions"><button v-for="hint in hints" :key="hint" @click="selectHint(hint)">{{ hint }}<Icon name="chevron" :size="14" /></button></div></div>
      <article v-for="message in current.messages" :key="message.id" class="message" :class="message.role"><div class="message-avatar"><Icon v-if="message.role === 'assistant'" name="grid" :size="16" /><Icon v-else-if="message.role === 'instruction'" name="branch" :size="16" /><span v-else>{{ login[0]?.toUpperCase() }}</span></div><div class="message-content"><div class="message-label">{{ message.role === 'assistant' ? 'AI Factory' : message.role === 'instruction' ? '追加指示' : 'あなた' }}<span v-if="message.role === 'instruction'" class="instruction-state">{{ message.applied ? '反映済み' : '受付済み・次の区切りで反映' }}</span></div><div class="message-text">{{ message.text }}</div></div></article>
      <SourceFiles v-if="!current.review" :key="current.id" :session="current" @open="openFile" @clear="closeFile" />
      <div v-if="running" class="progress-message"><span class="loading-dots">•••</span>{{ current.state === 'stopping' ? '停止を要求しています…' : work.progress || '処理を開始しています…' }}</div>
     </div>
     <div class="composer-area">
      <div v-if="work.reconnecting" class="connection-notice">再接続中です。保存した会話を復元しています。</div>
      <div v-if="current.state === 'failed'" class="connection-notice" role="alert">{{ errorMessage(current.limitations.at(-1) || 'run_failed') }}</div>
      <div v-if="current.state === 'user_wait'" class="connection-notice" role="status">今回の実行時間または利用量の上限に達したため一時停止しました。途中結果は保存済みです。<template v-if="current.review">{{ current.review.steps.reduce((n, step) => n + step.files.filter(file => file.done).length, 0) }} / {{ current.review.totalFiles }} ファイルを処理済みです。</template>「保存済みの状態から再開」で残りを続行できます。</div>
      <div v-if="['auth_wait', 'failed', 'stopped', 'user_wait'].includes(current.state) && (current.messages.length || current.runKind === 'review')" class="resume-row"><span>保存した区切りから続行できます。</span><button class="plain-button" :disabled="running || !work.connected || (config.aiProvider !== 'demo' && !keyValid)" @click="resume"><Icon name="refresh" :size="13" />保存済みの状態から再開</button></div>
      <div class="composer"><textarea v-model="text" :placeholder="mode === 'instruction' ? '次の区切りで反映する追加指示…' : '質問や「このPRをレビューして」など…'" aria-label="メッセージ" rows="2" maxlength="4096" @keydown="keydown" /><div class="composer-toolbar"><div><select v-model="mode" aria-label="メッセージの種類"><option value="message">チャット</option><option value="instruction">追加指示</option></select><span class="composer-model">{{ current.model }}</span></div><button v-if="running" class="stop-button" :disabled="current.state === 'stopping'" @click="stop"><Icon name="stop" :size="13" />停止</button><button class="send-button" :disabled="!canSend" aria-label="送信" @click="send"><Icon name="arrow" :size="17" /></button></div></div>
      <div class="composer-caption"><span><span class="status-dot" :class="{ offline: !work.connected }"></span>{{ work.connected ? '接続中' : '未接続' }} · 読み取り専用<span v-if="current.activePullRequest"> · 対象 PR #{{ current.activePullRequest }}</span></span><span>Enterで送信</span></div>
     </div>
    </section>
    <aside v-if="openedFile" ref="codePanel" class="code-panel" aria-label="コードパネル" tabindex="-1">
     <CodeViewer :key="fileKey(openedFile)" :file="openedFile" panel @close="closeFile" />
    </aside>
    <aside v-else-if="showDetails" class="context-panel">
     <div class="context-title"><Icon name="grid" :size="16" /><h2>セッションの情報</h2></div>
     <div class="context-block"><h3>対象リポジトリ</h3><strong class="repo-name">{{ current.target.repository }}</strong><div class="commit"><Icon name="branch" :size="13" /><code>{{ current.target.commit.slice(0, 12) }}</code><span>固定</span></div><p>{{ current.review ? 'レビューは表示されたベース・ヘッドを固定して確認します。コメントは取得時点の情報です。' : 'ファイルはこのコミットを参照します。PR・Issueは取得時点の情報です。' }}</p></div>
     <div class="context-block"><h3>セッションの利用量</h3><dl class="metrics"><div><dt>入力トークン</dt><dd>{{ (current.metrics.inputTokens || 0).toLocaleString() }}</dd></div><div><dt>出力トークン</dt><dd>{{ (current.metrics.outputTokens || 0).toLocaleString() }}</dd></div><div><dt>合計トークン</dt><dd>{{ ((current.metrics.inputTokens || 0) + (current.metrics.outputTokens || 0)).toLocaleString() }}</dd></div><div><dt>概算コスト（USD）</dt><dd>{{ sessionCost === undefined ? '算出できません' : formatCost(sessionCost) }}</dd></div></dl><p v-if="sessionPricing">標準料金での概算です。キャッシュ割引や無料枠などは反映していません。<br /><a :href="sessionPricing.url" target="_blank" rel="noopener noreferrer">料金表を確認 ↗</a></p><p v-else>このモデルの料金情報がないため、コストは算出できません。</p></div>
     <div class="context-block"><h3>コンテキストの利用状況</h3><div class="meter-label"><span>最大入力データ量</span><strong>{{ (current.metrics.maxInputBytes / 1024).toFixed(1) }}<small> / {{ inputLimitBytes / 1024 }} KiB</small></strong></div><div class="meter"><span :style="{ width: Math.min(100, current.metrics.maxInputBytes / inputLimitBytes * 100) + '%' }"></span></div><dl class="metrics"><div><dt>GitHub取得回数</dt><dd>{{ current.metrics.toolCalls }} 回</dd></div><div><dt>取得サイズ</dt><dd>{{ (current.metrics.fetchedBytes / 1024).toFixed(1) }} KiB</dd></div><div><dt>重複取得の抑制</dt><dd>{{ current.metrics.cacheHits }} 回</dd></div><div><dt>直近の処理時間</dt><dd>{{ (current.metrics.durationMs / 1000).toFixed(1) }} 秒</dd></div><div><dt>プロセスのヒープ観測最大</dt><dd>{{ (current.metrics.heapPeakBytes / 1048576).toFixed(1) }} MiB</dd></div></dl><p>{{ current.runKind === 'review' ? 'PRは最大200ファイルを計画し、関連する差分を最大10ファイルまとめて自動で確認します。中断した場合は保存した区切りから再開できます。' : '1質問あたり最大6回・5ファイル。取得結果は一時保持し、履歴全体はAIに渡しません。' }}</p><p v-if="current.runKind === 'review'">入力は差分約{{ reviewPolicy.diffTokens.toLocaleString() }}・全体約{{ reviewPolicy.inputTokens.toLocaleString() }}トークンを目安に調整します（共通トークナイザーによる推定）。1回の継続実行は最大{{ reviewPolicy.maxMinutes }}分・AI{{ reviewPolicy.maxCalls }}回・入力{{ reviewPolicy.runInputTokens.toLocaleString() }}／出力{{ reviewPolicy.runOutputTokens.toLocaleString() }}トークン。停止・切断・上限到達まで自動で続けます。</p></div>
     <div class="context-block"><h3>参照元<span class="source-count">{{ current.sources?.length || 0 }}</span></h3><p v-if="!current.sources?.length">取得した情報の参照元が表示されます。</p><a v-for="source in current.sources" :key="source.url" class="source-link" :href="safeLink(source.url)" target="_blank" rel="noopener noreferrer"><Icon name="link" :size="13" /><span>{{ source.label }}</span><Icon name="external" :size="12" /></a></div>
     <details v-if="current.summary" class="context-block"><summary>引き継ぎ用の要点</summary><p class="summary-text">{{ current.summary }}</p></details>
     <div v-if="!current.review && current.limitations?.length" class="context-block limitations"><h3>未確認の範囲・制限</h3><p v-for="(limitation, i) in current.limitations" :key="i">{{ errors[limitation] || limitation }}</p></div>
     <div class="retention"><Icon name="clock" :size="14" /><span>会話の保存期限 {{ date(current.expiresAt) }}<br />会話はいつでも削除できます。</span></div>
     <button class="plain-button context-delete" @click="deleteID = current.id"><Icon name="trash" :size="12" />このセッションを削除</button>
     <div v-if="deleteID === current.id" class="delete-confirm">会話と結果を削除します。<button @click="action(async () => { if (current) await work.remove(current.id); deleteID = ''; })">削除する</button></div>
    </aside>
   </div>
  </main>
 </div>
</template>
