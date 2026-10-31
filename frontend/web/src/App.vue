<script setup lang="ts">
import { computed, nextTick, onMounted, onUnmounted, ref, watch } from 'vue';
import { api, auth, loginComplete, logout } from './lib/auth';
import { clearKey, credentials, setKey } from './lib/keyVault';
import { useWorkspace } from './stores/workspace';
import { busy, states, type Config, type Repository } from './types';
import Icon from './components/Icon.vue';

const work = useWorkspace();
const config = ref<Config>({ demo: false, models: [], installUrl: '' });
const login = ref(''); const loading = ref(true); const actionPending = ref(false); const appError = ref('');
const repositories = ref<Repository[]>([]); const target = ref(''); const model = ref(''); const text = ref('');
const keyDraft = ref(''); const keyValid = ref(false); const remaining = ref(0); const consent = ref(false); const mode = ref('message');
const showNew = ref(false); const showDetails = ref(window.matchMedia('(min-width: 1100px)').matches); const deleteID = ref(''); const transcript = ref<HTMLElement>();
const current = computed(() => work.current); const running = computed(() => busy(current.value));
const canSend = computed(() => !!text.value.trim() && work.connected && (mode.value === 'instruction' || (!running.value && (config.value.demo || keyValid.value))));
const hints = ['このリポジトリの構成を教えて', 'READMEからセットアップ手順を整理して', '公開中のIssueを確認したい'];
const errors: Record<string, string> = {
 login_required: 'ログインの有効期限が切れました。GitHubでログインし直してください。', github_login_required: 'GitHubでログインしてください。',
 repository_forbidden: 'このリポジトリを参照できません。GitHub Appのアクセス許可を確認してください。',
 session_busy: '別の接続でセッションを使用中です。接続を閉じて、最大45秒ほど待ってください。', server_busy: '接続数の上限に達しています。少し待ってから再接続してください。',
 ai_key_required: 'OpenAI APIキーを入力してください。有効期間は入力から最大1時間です。',
 connection_failed: '接続できませんでした。APIの起動状態を確認してください。', connection_required: '再接続してから送信してください。',
 invalid_message_no_secrets: '本文は4,096バイト以内にし、APIキーなどの認証情報を含めないでください。',
 invalid_instruction: '追加指示は4,096バイト以内にし、認証情報を含めないでください。', instruction_limit: '追加指示の合計上限に達しました。新しいセッションを作成してください。',
 history_limit_create_new_session: '会話の保存上限に達しました。新しいセッションを作成してください。',
 invalid_repository: 'owner/repository またはGitHubのリポジトリ・PR・Issue URLを指定してください。', invalid_github_url: 'GitHub.comのリポジトリ・PR・Issue URLを指定してください。',
 store_unavailable: '保存先に接続できません。Firebase EmulatorまたはFirestoreを確認してください。', openai_http_401: 'OpenAI APIキーを確認してください。', openai_http_429: 'OpenAIの利用枠またはレート制限に達しました。',
};
function errorMessage(value: unknown) { const code = value instanceof Error ? value.message : String(value); return errors[code] || `処理を完了できませんでした（${code}）。`; }
async function action(fn: () => Promise<void>) { if (actionPending.value) return; actionPending.value = true; appError.value = ''; try { await fn(); } catch (e) { appError.value = errorMessage(e); } finally { actionPending.value = false; } }
async function loadWorkspace() { const me = await api<{ login: string }>('/me'); login.value = me.login; await work.refresh(); await refreshRepositories(); }
async function refreshRepositories() { repositories.value = await api<Repository[]>('/repositories'); if (!target.value) target.value = repositories.value[0]?.full_name || ''; }
async function signIn() { await action(async () => { if (config.value.demo) { await loginComplete(); await loadWorkspace(); } else { location.assign('/auth/github'); } }); }
async function signOut() { await action(async () => { work.disconnect(true); keyDraft.value = ''; clearKey(); await logout(); login.value = ''; work.sessions = []; }); }
async function createSession() { await action(async () => { const s = await api<NonNullable<typeof current.value>>('/sessions', { method: 'POST', body: JSON.stringify({ target: target.value, model: model.value }) }); await work.open(s); showNew.value = false; consent.value = false; keyDraft.value = ''; syncKey(); }); }
function applyKey() { appError.value = ''; if (!consent.value || !keyDraft.value.startsWith('sk-') || keyDraft.value.length < 20) { appError.value = '送信内容を確認し、有効なOpenAI APIキーを入力してください。'; return; } setKey(keyDraft.value.trim()); keyDraft.value = ''; syncKey(); }
function syncKey() { const c = credentials(); keyValid.value = !!c.apiKey; remaining.value = Math.max(0, Math.ceil((c.keyExpiresAt - Date.now()) / 60000)); }
function send() { if (!canSend.value) return; try { work.send(mode.value, text.value.trim()); text.value = ''; } catch (e) { appError.value = errorMessage(e); } }
function resume() { try { work.send('resume'); } catch (e) { appError.value = errorMessage(e); } }
function stop() { try { work.send('stop'); } catch (e) { appError.value = errorMessage(e); } }
function end() { try { if (work.connected) work.send('end'); } finally { work.disconnect(true); keyDraft.value = ''; syncKey(); } }
function selectHint(hint: string) { text.value = hint; }
function keydown(event: KeyboardEvent) { if (event.key === 'Enter' && !event.shiftKey && !event.isComposing) { event.preventDefault(); send(); } }
function safeLink(url: string) { try { const u = new URL(url); return u.protocol === 'https:' && u.hostname === 'github.com' ? u.href : '#'; } catch { return '#'; } }
function date(value: string) { return new Intl.DateTimeFormat('ja-JP', { month: 'short', day: 'numeric' }).format(new Date(value)); }
let keyTimer: ReturnType<typeof setInterval>;
const pagehide = () => { clearKey(); work.disconnect(true); };
onMounted(async () => {
 keyTimer = setInterval(syncKey, 1000); window.addEventListener('pagehide', pagehide);
 try { const response = await fetch('/api/config'); if (!response.ok) throw new Error('APIの設定を取得できません'); config.value = await response.json(); model.value = config.value.models[0] || ''; await auth.authStateReady();
  if (new URLSearchParams(location.search).has('login')) { history.replaceState({}, '', '/'); await loginComplete(); }
  if (auth.currentUser) await loadWorkspace();
 } catch (e) { appError.value = errorMessage(e); } finally { loading.value = false; }
});
onUnmounted(() => { clearInterval(keyTimer); window.removeEventListener('pagehide', pagehide); clearKey(); work.disconnect(true); });
watch(() => current.value?.messages.length, async () => { await nextTick(); transcript.value?.scrollTo({ top: transcript.value.scrollHeight, behavior: 'smooth' }); });
watch(() => current.value?.id, () => { keyDraft.value = ''; text.value = ''; consent.value = false; syncKey(); });
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
   <header class="topbar"><div class="breadcrumb">ワークスペース<Icon name="chevron" :size="13" /><strong>GitHub チャット</strong></div><div class="top-actions"><span class="badge" :class="{ demo: config.demo }"><span class="status-dot"></span>{{ config.demo ? 'デモモード' : 'READ ONLY' }}</span><button v-if="current" class="icon-button" aria-label="参照情報の表示を切り替え" @click="showDetails = !showDetails"><Icon name="grid" :size="17" /></button></div></header>
   <div v-if="appError || work.error" class="error-banner" role="alert">{{ appError || errorMessage(work.error) }}<button class="plain-button" @click="appError = ''; work.error = ''">閉じる</button></div>
   <div v-if="config.demo" class="demo-banner">固定デモで操作を確認できます。実際のGitHub・OpenAIには接続しません。</div>

   <div v-if="!current || showNew" class="welcome-scroll">
    <div class="welcome">
     <div class="eyebrow"><span class="green-line"></span> YOUR REPOSITORY, IN CONTEXT</div>
     <h1>コードを読み解く、<br /><span>次の会話をはじめよう。</span></h1>
     <p class="welcome-description">リポジトリの構成も、PRの背景も、気になるIssueも。<br />GitHubの情報を必要な分だけ集めて、根拠とともに整理します。</p>
     <div class="setup-card">
      <div class="card-heading"><div class="number-label">01</div><div><h2>{{ login ? '調べたいリポジトリを選択' : 'GitHubとつなげて、はじめる' }}</h2><p>{{ login ? '許可したリポジトリ・PR・Issueを参照できます。' : 'GitHub Appで、参照するリポジトリを選べます。' }}</p></div><Icon name="branch" :size="22" /></div>
      <div v-if="!login" class="login-area"><button class="primary-button" :disabled="loading || actionPending" @click="signIn"><Icon name="code" :size="18" />{{ config.demo ? '開発用ログイン' : 'GitHubでログイン' }}<Icon name="chevron" :size="16" /></button><span>コードの変更や投稿は行いません。</span></div>
      <form v-else @submit.prevent="createSession">
       <label for="target">リポジトリ / PR / Issue</label><div class="input-with-icon"><Icon name="link" :size="17" /><input id="target" v-model="target" list="repos" placeholder="owner/repository または https://github.com/…" required maxlength="1024" /></div>
       <datalist id="repos"><option v-for="r in repositories" :key="r.full_name" :value="r.full_name" /></datalist>
       <div class="repository-actions"><span>{{ repositories.length }} 件のリポジトリに接続</span><button class="plain-button" type="button" @click="action(refreshRepositories)"><Icon name="refresh" :size="12" />再取得</button><a v-if="!config.demo" :href="config.installUrl" target="_blank" rel="noopener noreferrer">アクセスを設定<Icon name="external" :size="12" /></a></div>
       <div class="form-bottom"><div class="model-select"><label for="model">AIモデル</label><select id="model" v-model="model"><option v-for="m in config.models" :key="m">{{ m }}</option></select></div><button class="primary-button" :disabled="actionPending || !target"><span>セッションを作成</span><Icon name="chevron" :size="16" /></button></div>
      </form>
     </div>
     <div class="feature-grid"><div><Icon name="link" :size="20" /><h3>根拠から、わかる。</h3><p>参照元と未確認の範囲を<br />一緒に確認できます。</p></div><div><Icon name="grid" :size="20" /><h3>必要な情報だけ。</h3><p>会話と取得情報を分け、<br />コンテキストを小さく保ちます。</p></div><div><Icon name="key" :size="20" /><h3>キーは、一時的に。</h3><p>自分のOpenAI APIキーを使用。<br />入力から最大1時間で破棄します。</p></div></div>
     <div class="welcome-footer"><span class="small-orbit">✳</span> 小さく読み取り、理解を深める。<span>PHASE 01 — GITHUB CHAT</span></div>
    </div>
   </div>

   <div v-else class="conversation-layout" :class="{ 'details-hidden': !showDetails }">
    <section class="conversation">
     <div class="conversation-header"><div><span class="section-caption">GITHUB SESSION</span><h2><Icon name="folder" :size="19" />{{ current.target.repository }}</h2></div><div><span class="session-state" :class="{ pulse: running }">{{ states[current.state] }}</span><button class="plain-button" @click="end">終了</button></div></div>
     <div v-if="!config.demo && !keyValid" class="key-card">
      <div class="key-heading"><Icon name="key" :size="17" /><strong>OpenAI APIキーを接続</strong><span>このセッションのみ</span></div>
      <p>回答の生成時に、非公開コードを含む取得情報をOpenAIへ送信します。API利用料はご自身のOpenAIアカウントに発生します。ChatGPTの契約とは別です。</p>
      <label class="consent"><input v-model="consent" type="checkbox" />送信内容と費用について確認しました</label>
      <form class="key-form" @submit.prevent="applyKey"><input v-model="keyDraft" type="password" autocomplete="off" spellcheck="false" placeholder="sk-…" aria-label="OpenAI APIキー" maxlength="512" /><button class="primary-button" :disabled="!consent">接続する</button></form>
     </div>
     <div ref="transcript" class="transcript" role="log" aria-label="会話履歴" aria-live="polite">
      <div v-if="!current.messages.length" class="chat-empty"><span class="assistant-mark"><Icon name="grid" :size="24" /></span><h2>何から読み解きましょう？</h2><p>構成やファイル、PR・Issueについて質問してください。</p><div class="suggestions"><button v-for="hint in hints" :key="hint" @click="selectHint(hint)">{{ hint }}<Icon name="chevron" :size="14" /></button></div></div>
      <article v-for="message in current.messages" :key="message.id" class="message" :class="message.role"><div class="message-avatar"><Icon v-if="message.role === 'assistant'" name="grid" :size="16" /><Icon v-else-if="message.role === 'instruction'" name="branch" :size="16" /><span v-else>{{ login[0]?.toUpperCase() }}</span></div><div class="message-content"><div class="message-label">{{ message.role === 'assistant' ? 'AI Factory' : message.role === 'instruction' ? '追加指示' : 'あなた' }}<span v-if="message.role === 'instruction'" class="instruction-state">{{ message.applied ? '反映済み' : '受付済み・次の区切りで反映' }}</span></div><div class="message-text">{{ message.text }}</div></div></article>
      <div v-if="running" class="progress-message"><span class="loading-dots">•••</span>{{ current.state === 'stopping' ? '停止を要求しています…' : work.progress || '処理を開始しています…' }}</div>
     </div>
     <div class="composer-area">
      <div v-if="work.reconnecting" class="connection-notice">再接続中です。保存した会話を復元しています。</div>
      <div v-if="['auth_wait', 'failed', 'stopped'].includes(current.state) && current.messages.length" class="resume-row"><span>続行には確認操作が必要です。</span><button class="plain-button" :disabled="running || !work.connected || (!config.demo && !keyValid)" @click="resume"><Icon name="refresh" :size="13" />保存済みの状態から再開</button></div>
      <div class="composer"><textarea v-model="text" :placeholder="mode === 'instruction' ? '次の区切りで反映する追加指示…' : 'このリポジトリについて質問する…'" aria-label="メッセージ" rows="2" maxlength="4096" @keydown="keydown" /><div class="composer-toolbar"><div><select v-model="mode" aria-label="メッセージの種類"><option value="message">質問する</option><option value="instruction">追加指示</option></select><span class="composer-model">{{ current.model }}</span></div><button v-if="running" class="stop-button" :disabled="current.state === 'stopping'" @click="stop"><Icon name="stop" :size="13" />停止</button><button class="send-button" :disabled="!canSend" aria-label="送信" @click="send"><Icon name="arrow" :size="17" /></button></div></div>
      <div class="composer-caption"><span><span class="status-dot" :class="{ offline: !work.connected }"></span>{{ work.connected ? '接続中' : '未接続' }} · 読み取り専用</span><span>Enterで送信 · Shift + Enterで改行</span></div>
     </div>
    </section>
    <aside v-if="showDetails" class="context-panel">
     <div class="context-title"><Icon name="grid" :size="16" /><h2>セッションの情報</h2></div>
     <div class="context-block"><h3>対象リポジトリ</h3><strong class="repo-name">{{ current.target.repository }}</strong><div class="commit"><Icon name="branch" :size="13" /><code>{{ current.target.commit.slice(0, 12) }}</code><span>固定</span></div><p>ファイルはこのコミットを参照します。PR・Issueは取得時点の情報です。</p></div>
     <div class="context-block"><h3>AI接続</h3><div class="key-status"><Icon :name="config.demo ? 'code' : keyValid ? 'check' : 'key'" :size="15" />{{ config.demo ? '固定デモ' : keyValid ? `APIキー接続中 · 残り${remaining}分` : 'APIキー未入力' }}</div><button v-if="keyValid" class="plain-button" @click="clearKey(); syncKey()">キーを破棄</button><p>キーは永続保存しません。セッション終了・ログアウトで破棄します。</p></div>
     <div class="context-block"><h3>コンテキストの利用状況<span class="tiny-tag">累計</span></h3><div class="meter-label"><span>最大入力データ量</span><strong>{{ (current.metrics.maxInputBytes / 1024).toFixed(1) }}<small> / 24 KiB</small></strong></div><div class="meter"><span :style="{ width: Math.min(100, current.metrics.maxInputBytes / 24576 * 100) + '%' }"></span></div><dl class="metrics"><div><dt>入力 / 出力トークン</dt><dd>{{ current.metrics.inputTokens.toLocaleString() }} / {{ current.metrics.outputTokens.toLocaleString() }}</dd></div><div><dt>GitHub取得回数</dt><dd>{{ current.metrics.toolCalls }} 回</dd></div><div><dt>取得サイズ</dt><dd>{{ (current.metrics.fetchedBytes / 1024).toFixed(1) }} KiB</dd></div><div><dt>重複取得の抑制</dt><dd>{{ current.metrics.cacheHits }} 回</dd></div><div><dt>直近の処理時間</dt><dd>{{ (current.metrics.durationMs / 1000).toFixed(1) }} 秒</dd></div><div><dt>プロセスのヒープ観測最大</dt><dd>{{ (current.metrics.heapPeakBytes / 1048576).toFixed(1) }} MiB</dd></div></dl><p>1質問あたり最大6回・5ファイル。取得結果は一時保持し、履歴全体はAIに渡しません。</p></div>
     <div class="context-block"><h3>参照元<span class="source-count">{{ current.sources?.length || 0 }}</span></h3><p v-if="!current.sources?.length">取得した情報の参照元が表示されます。</p><a v-for="source in current.sources" :key="source.url" class="source-link" :href="safeLink(source.url)" target="_blank" rel="noopener noreferrer"><Icon name="link" :size="13" /><span>{{ source.label }}</span><Icon name="external" :size="12" /></a></div>
     <details v-if="current.summary" class="context-block"><summary>引き継ぎ用の要点</summary><p class="summary-text">{{ current.summary }}</p></details>
     <div v-if="current.limitations?.length" class="context-block limitations"><h3>未確認の範囲・制限</h3><p v-for="(limitation, i) in current.limitations" :key="i">{{ errors[limitation] || limitation }}</p></div>
     <div class="retention"><Icon name="clock" :size="14" /><span>会話の保存期限 {{ date(current.expiresAt) }}<br />会話はいつでも削除できます。</span></div>
     <button class="plain-button context-delete" @click="deleteID = current.id"><Icon name="trash" :size="12" />このセッションを削除</button>
     <div v-if="deleteID === current.id" class="delete-confirm">会話と結果を削除します。<button @click="action(async () => { if (current) await work.remove(current.id); deleteID = ''; })">削除する</button></div>
    </aside>
   </div>
  </main>
 </div>
</template>
