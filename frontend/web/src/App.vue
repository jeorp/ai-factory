<script setup lang="ts">
import { computed, defineAsyncComponent, nextTick, onMounted, onUnmounted, ref, watch } from 'vue';
import { api, auth, loginComplete, logout } from './lib/auth';
import { clearKey } from './lib/keyVault';
import { clearFiles } from './lib/fileCache';
import { errorMessage } from './lib/errorMessages';
import { fileKey, type SourceFile } from './lib/sourceFiles';
import { reviewTimeline } from './lib/reviewTimeline';
import { defaultReviewPolicy, type ComposerMode } from './lib/sessionView';
import { useApiKey } from './composables/useApiKey';
import { useWorkspace } from './stores/workspace';
import { busy, states, type Config, type Repository, type Session } from './types';
import Icon from './components/Icon.vue';
import EffortSelect from './components/EffortSelect.vue';
import AppSidebar from './components/AppSidebar.vue';
import WelcomePanel from './components/WelcomePanel.vue';
import ApiKeyCard from './components/ApiKeyCard.vue';
import ReviewResults from './components/ReviewResults.vue';
import ChatMessage from './components/ChatMessage.vue';
import SourceFiles from './components/SourceFiles.vue';
import ImplementationPanel from './components/ImplementationPanel.vue';
import SessionComposer from './components/SessionComposer.vue';
import SessionContextPanel from './components/SessionContextPanel.vue';

const CodeViewer = defineAsyncComponent(() => import('./components/CodeViewer.vue'));

const work = useWorkspace();
const current = computed(() => work.current);
const running = computed(() => busy(current.value));
const timeline = computed(() => reviewTimeline(current.value?.messages || [], current.value?.review));

// App-wide state.
const config = ref<Config>({ demo: false, aiProvider: 'openai', models: [], installUrl: '' });
const reviewPolicy = computed(() => config.value.reviewPolicy || defaultReviewPolicy);
const login = ref(''); const loading = ref(true); const actionPending = ref(false); const appError = ref('');
const showNew = ref(false); const showDetails = ref(window.matchMedia('(min-width: 1100px)').matches); const deleteID = ref('');
const transcript = ref<HTMLElement>();

// New session form.
const repositories = ref<Repository[]>([]); const repositoryChoice = ref(''); const manualTarget = ref(''); const model = ref('');
const effort = ref('medium');
const newEfforts = computed(() => config.value.modelEfforts?.[model.value] || []);
watch(newEfforts, levels => { if (!levels.includes(effort.value)) effort.value = levels.includes('medium') ? 'medium' : levels[0] || ''; });
const currentEfforts = computed(() => config.value.modelEfforts?.[current.value?.model || ''] || []);
const currentEffort = computed(() => current.value?.effort || (currentEfforts.value.includes('medium') ? 'medium' : currentEfforts.value[0] || ''));
const target = computed(() => repositoryChoice.value === '__manual__' ? manualTarget.value.trim() : repositoryChoice.value);

// AI key and composer.
// With several providers configured, the key belongs to the provider of the open session's model.
const aiProvider = computed(() => config.value.modelProviders?.[current.value?.model || model.value] || config.value.aiProvider);
const keys = useApiKey({ provider: () => aiProvider.value, sessionId: () => current.value?.id, onError: message => { appError.value = message; } });
watch(aiProvider, (now, before) => { if (before && now !== before) { keys.discard(); keys.resetForm(); } });
const canUseAI = computed(() => aiProvider.value === 'demo' || keys.valid);
const canRun = computed(() => work.connected && canUseAI.value);
const text = ref(''); const mode = ref<ComposerMode>('message'); const fixPull = ref('');
const canSend = computed(() => !!text.value.trim() && work.connected && (mode.value === 'instruction' || (!running.value && canUseAI.value)));
const hints = ['このリポジトリの構成を教えて', 'READMEからセットアップ手順を整理して', 'PR一覧を見せて', '取得したPRをレビューして', 'READMEにセットアップ手順を追加するPRを作って'];

// Code panel: focus moves into the panel and back to the control that opened it.
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
function toggleDetails() { openedFile.value = undefined; showDetails.value = !showDetails.value; }

/** Runs one user action at a time and shows its failure in the error banner. */
async function action(fn: () => Promise<void>) {
 if (actionPending.value) return;
 actionPending.value = true; appError.value = '';
 try { await fn(); } catch (e) { appError.value = errorMessage(e); } finally { actionPending.value = false; }
}
function dismissError() { appError.value = ''; work.error = ''; }

async function loadWorkspace() { const me = await api<{ login: string }>('/me'); login.value = me.login; await work.refresh(); await refreshRepositories(); }
async function refreshRepositories() {
 repositories.value = await api<Repository[]>('/repositories');
 if (repositoryChoice.value && repositoryChoice.value !== '__manual__' && !repositories.value.some(r => r.full_name === repositoryChoice.value)) repositoryChoice.value = '';
}
async function signIn() { await action(async () => { if (config.value.demo) { await loginComplete(); await loadWorkspace(); } else { location.assign('/auth/github'); } }); }
async function signOut() {
 await action(async () => {
  const owner = auth.currentUser?.uid;
  work.disconnect(true); keys.draft = ''; clearKey();
  await nextTick(); if (owner) await clearFiles(owner).catch(() => {});
  await logout();
  login.value = ''; work.sessions = []; repositories.value = []; repositoryChoice.value = ''; manualTarget.value = '';
 });
}
async function createSession() {
 await action(async () => {
  const s = await api<Session>('/sessions', { method: 'POST', body: JSON.stringify({ target: target.value, model: model.value, ...(newEfforts.value.length ? { effort: effort.value } : {}) }) });
  await work.open(s);
  showNew.value = false; repositoryChoice.value = ''; manualTarget.value = ''; keys.consent = false; keys.draft = ''; keys.sync();
 });
}
const openSession = (s: Session) => action(async () => { await work.open(s); showNew.value = false; });
const removeSession = (id: string) => action(async () => { await work.remove(id); deleteID.value = ''; });

/** Sends a socket message, reporting a dropped connection in the error banner. */
function sendToSession(...args: Parameters<typeof work.send>) { try { work.send(...args); } catch (e) { appError.value = errorMessage(e); } }
function send() {
 if (!canSend.value) return;
 const pull = mode.value === 'implement' ? Number.parseInt(fixPull.value, 10) : 0;
 try { work.send(mode.value, text.value.trim(), crypto.randomUUID(), undefined, pull > 0 ? { pullNumber: pull } : undefined); text.value = ''; }
 catch (e) { appError.value = errorMessage(e); }
}
const setEffort = (value: string) => sendToSession('set_effort', '', crypto.randomUUID(), undefined, { effort: value });
const investigate = (index: number) => sendToSession('investigate', '', crypto.randomUUID(), index);
const amendImplementation = (approve: boolean) => sendToSession('implement_amend', approve ? 'approve' : 'decline');
const publishImplementation = (value: { branch: string; title: string; body: string }) => sendToSession('implement_publish', '', crypto.randomUUID(), undefined, value);
function end() { try { if (work.connected) work.send('end'); } finally { work.disconnect(true); keys.draft = ''; keys.sync(); } }

let keyTimer: ReturnType<typeof setInterval>;
const pagehide = () => { clearKey(); work.disconnect(true); };
onMounted(async () => {
 keyTimer = setInterval(keys.sync, 1000); window.addEventListener('pagehide', pagehide); window.addEventListener('keydown', escapeFile);
 try {
  const response = await fetch('/api/config'); if (!response.ok) throw new Error('APIの設定を取得できません');
  config.value = await response.json(); await keys.refreshSaved(); model.value = config.value.models[0] || ''; await auth.authStateReady();
  if (new URLSearchParams(location.search).has('login')) { history.replaceState({}, '', '/'); await loginComplete(); }
  if (auth.currentUser) await loadWorkspace();
 } catch (e) { appError.value = errorMessage(e); } finally { loading.value = false; }
});
onUnmounted(() => { clearInterval(keyTimer); window.removeEventListener('pagehide', pagehide); window.removeEventListener('keydown', escapeFile); clearKey(); work.disconnect(true); });
watch(() => current.value?.messages.length, async () => { await nextTick(); transcript.value?.scrollTo({ top: transcript.value.scrollHeight, behavior: 'smooth' }); });
// A run that fails on a rejected key drops the key so the card asks for a new one.
watch(() => [current.value?.id, current.value?.state] as const, ([id, state], [previousId, previous]) => {
 const rejected = ['gemini_key_invalid', 'openai_http_401', 'claude_token_invalid', 'claude_token_expired', 'anthropic_key_invalid'].includes(current.value?.limitations?.at(-1) || '');
 if (id === previousId && state === 'failed' && previous && ['running', 'accepted', 'stopping'].includes(previous) && rejected) {
  keys.discard(); appError.value = 'APIキーが無効です。保存済みのキーを削除するか、新しいキーを入力してください。';
 }
});
watch(() => current.value?.id, () => { text.value = ''; keys.resetForm(); });
watch([() => current.value?.id, showNew], () => { openedFile.value = undefined; fileOpener = undefined; });
</script>

<template>
 <div class="app-shell">
  <AppSidebar v-model:delete-id="deleteID" :login="login" :demo="config.demo" :sessions="work.sessions" :current-id="current?.id" @create="showNew = true" @open="openSession" @remove="removeSession" @sign-out="signOut" />

  <main class="main-panel">
   <header class="topbar"><div class="breadcrumb">ワークスペース<Icon name="chevron" :size="13" /><strong>GitHub チャット</strong></div><div class="top-actions"><span class="badge" :class="{ demo: config.demo }"><span class="status-dot"></span>{{ config.demo ? 'デモモード' : 'PR作成は確認後のみ' }}</span><button v-if="current" class="icon-button" aria-label="参照情報の表示を切り替え" @click="toggleDetails"><Icon name="grid" :size="17" /></button></div></header>
   <div v-if="appError || work.error" class="error-banner" role="alert">{{ appError || errorMessage(work.error) }}<button class="plain-button" @click="dismissError">閉じる</button></div>
   <div v-if="config.demo" class="demo-banner">{{ config.aiProvider === 'demo' ? '固定デモです。実際のGitHubやAIには接続しません。' : 'GitHubは固定デモです。質問とサンプル情報はGeminiへ送信されます。' }}</div>

   <WelcomePanel v-if="!current || showNew" v-model:repository-choice="repositoryChoice" v-model:manual-target="manualTarget" v-model:model="model" v-model:effort="effort" :login="login" :config="config" :repositories="repositories" :loading="loading" :action-pending="actionPending" :target="target" @sign-in="signIn" @create="createSession" @refresh-repositories="action(refreshRepositories)" />

   <div v-else class="conversation-layout" :class="{ 'details-hidden': !showDetails && !openedFile, 'code-open': !!openedFile }">
    <section class="conversation">
     <div class="conversation-header"><div><span class="section-caption">GITHUB SESSION</span><h2><Icon name="folder" :size="19" />{{ current.target.repository }}</h2></div><div><span class="session-state" :class="{ pulse: running }">{{ states[current.state] }}</span><button class="plain-button" @click="end">終了</button></div></div>
     <ApiKeyCard v-if="aiProvider !== 'demo' && !keys.valid" :provider="aiProvider" :keys="keys" />
     <div ref="transcript" class="transcript" role="log" aria-label="会話履歴" aria-live="polite">
      <div v-if="!current.messages.length && !current.review" class="chat-empty"><span class="assistant-mark"><Icon name="grid" :size="24" /></span><h2>何から読み解きましょう？</h2><p>PRを取得したあと、「このPRをレビューして」と続けて依頼できます。</p><div class="suggestions"><button v-for="hint in hints" :key="hint" @click="text = hint">{{ hint }}<Icon name="chevron" :size="14" /></button></div></div>
      <template v-for="entry in timeline" :key="`${current.id}:${entry.key}`">
       <ReviewResults v-if="entry.kind === 'review' && current.review" :session="current" :review="current.review" :demo="config.demo" :running="running" :can-run="canRun" @investigate="investigate" />
       <ChatMessage v-else-if="entry.kind === 'message'" :message="entry.message" :login="login" :repository="current.target.repository" />
      </template>
      <ImplementationPanel v-if="current.implementation" :key="`${current.id}:impl`" :session-id="current.id" :repository="current.target.repository" :implementation="current.implementation" :session-state="current.state" :running="running" :connected="work.connected" :can-run="canRun" @approve="sendToSession('implement_approve')" @resume="sendToSession('resume')" @amend="amendImplementation" @publish="publishImplementation" @check-ci="sendToSession('implement_check_ci')" @fix-ci="sendToSession('implement_fix_ci')" @verify="sendToSession('implement_verify')" />
      <SourceFiles v-if="!current.review" :key="current.id" :session="current" @open="openFile" @clear="closeFile" />
      <div v-if="running && current.implementation?.status !== 'checking_ci'" class="progress-message"><span class="loading-dots">•••</span>{{ current.state === 'stopping' ? '停止を要求しています…' : work.progress || '処理を開始しています…' }}</div>
     </div>
     <div v-if="currentEfforts.length" class="session-effort"><EffortSelect id="session-effort" :levels="currentEfforts" :value="currentEffort" :disabled="running || !work.connected" @change="setEffort" /><span>次の実行から適用 · 高いほど時間・費用が増える場合があります</span></div>
     <SessionComposer v-model:text="text" v-model:mode="mode" v-model:fix-pull="fixPull" :session="current" :running="running" :connected="work.connected" :reconnecting="work.reconnecting" :can-run="canRun" :can-send="canSend" @send="send" @stop="sendToSession('stop')" @resume="sendToSession('resume')" />
    </section>
    <aside v-if="openedFile" ref="codePanel" class="code-panel" aria-label="コードパネル" tabindex="-1">
     <CodeViewer :key="fileKey(openedFile)" :file="openedFile" panel @close="closeFile" />
    </aside>
    <SessionContextPanel v-else-if="showDetails" v-model:delete-id="deleteID" :session="current" :provider="aiProvider" :review-policy="reviewPolicy" @remove="removeSession" />
   </div>
  </main>
 </div>
</template>
