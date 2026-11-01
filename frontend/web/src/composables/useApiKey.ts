import { reactive } from 'vue';
import { clearKey, credentials, deleteSavedKey, savedKey, saveKey, setKey } from '../lib/keyVault';

interface Options {
 /** The AI provider whose key is managed. Read on every call so config changes apply. */
 provider: () => string;
 /** The open session. A key chosen for one session is dropped if the user switches meanwhile. */
 sessionId: () => string | undefined;
 onError: (message: string) => void;
}

/** State and actions behind the AI API key card. */
export function useApiKey({ provider, sessionId, onError }: Options) {
 const state = reactive({
  draft: '', choice: 'new' as 'saved' | 'new', pending: false, remember: false, hasSaved: false, valid: false, consent: false,
  apply, refreshSaved, forgetSaved, sync, discard, resetForm,
 });

 /** Mirrors whether a usable key is held in memory (keys expire on their own). */
 function sync() { state.valid = !!credentials().apiKey; }

 async function apply() {
  if (state.pending) return;
  onError(''); state.pending = true;
  const id = sessionId();
  try {
   const key = state.choice === 'saved' ? await savedKey(provider()) : state.draft.trim();
   if (id !== sessionId()) return;
   if (!state.consent || !key || key.length > 512 || /\s/.test(key)) { onError('送信内容を確認し、AIのAPIキーを選択または入力してください。'); return; }
   if (state.choice === 'new' && state.remember) { await saveKey(provider(), key); state.hasSaved = true; }
   if (id !== sessionId()) return;
   setKey(key); state.draft = ''; sync();
  } catch { onError('APIキーの保存または読み込みに失敗しました。ブラウザの保存設定を確認してください。'); }
  finally { state.pending = false; }
 }

 async function refreshSaved() {
  try { state.hasSaved = !!(await savedKey(provider())); state.choice = state.hasSaved ? 'saved' : 'new'; }
  catch { onError('保存済みのAPIキーを読み込めませんでした。ブラウザの保存設定を確認してください。'); }
 }

 async function forgetSaved() {
  try { await deleteSavedKey(provider()); state.hasSaved = false; state.choice = 'new'; state.draft = ''; state.remember = false; clearKey(); sync(); }
  catch { onError('保存済みのAPIキーを削除できませんでした。'); }
 }

 /** Drops the in-memory key so the user has to connect again. */
 function discard() { clearKey(); sync(); }

 /** Resets the card when another session is opened. */
 function resetForm() { state.draft = ''; state.consent = false; state.remember = false; void refreshSaved(); sync(); }

 return state;
}

export type ApiKeyState = ReturnType<typeof useApiKey>;
