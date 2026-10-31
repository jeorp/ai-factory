<script setup lang="ts">
import { computed, onUnmounted, ref, watch } from 'vue';
import { api, auth } from '../lib/auth';
import { clearFiles, fileTTL, findSaved, savedFiles, saveFile, type SavedFile } from '../lib/fileCache';
import { fileKey, listedFiles, type FileRef, type SourceFile } from '../lib/sourceFiles';
import type { Session } from '../types';

const props = defineProps<{ session: Session }>();
const emit = defineEmits<{ open: [file: SavedFile, opener?: HTMLElement]; clear: [] }>();
const files = computed(() => listedFiles(props.session));
const saved = ref<SavedFile[]>([]); const pending = ref(''); const saving = ref(false); const completed = ref(0); const errors = ref<Record<string, string>>({}); const storageError = ref('');
const controller = new AbortController(); let active = true;
const owner = auth.currentUser?.uid || ''; const sessionID = props.session.id;
function available() { return active && !!owner && auth.currentUser?.uid === owner; }
function cached(file: FileRef) { return findSaved(saved.value, file); }
async function restore() {
 try { const stored = await savedFiles(owner, sessionID); if (available()) saved.value = stored; }
 catch { if (available()) storageError.value = 'ブラウザーへの保存を利用できません。ブラウザーの保存設定を確認してください。'; }
}
watch(() => files.value.map(fileKey).join('\n'), () => void restore(), { immediate: true });
async function fetchFile(file: FileRef, open = true) {
 const opener = document.activeElement instanceof HTMLElement ? document.activeElement : undefined;
 const key = fileKey(file); const local = cached(file);
 if (local) { if (open) emit('open', local, opener); return; }
 pending.value = key; delete errors.value[key];
 try {
  const query = new URLSearchParams({ path: file.path, commit: file.commit });
  const result = await api<SourceFile>(`/sessions/${sessionID}/file?${query}`, { signal: controller.signal });
  if (!available()) return;
  const stored = await saveFile(owner, sessionID, result);
  if (!available()) return;
  saved.value = stored;
  const selected = findSaved(stored, file);
  if (open && selected) emit('open', selected, opener);
 } catch (e) {
  if (available()) errors.value[key] = e instanceof Error && e.message === 'login_required' ? '再ログインしてください。' : '取得・保存できませんでした。権限、ファイルの存在、サイズ、ブラウザーの保存設定を確認して再試行してください。';
 } finally { if (available()) pending.value = ''; }
}
async function saveAll() {
 saving.value = true; completed.value = 0;
 const batch = [...files.value];
 try { for (const file of batch) { if (!available()) break; await fetchFile(file, false); completed.value++; } }
 finally { saving.value = false; }
}
async function clear() {
 try { await clearFiles(owner, sessionID); saved.value = []; emit('clear'); storageError.value = ''; }
 catch { storageError.value = '保存したファイルを削除できませんでした。'; }
}
onUnmounted(() => { active = false; controller.abort(); saved.value = []; });
</script>

<template>
 <section v-if="files.length" class="source-files" aria-label="実装ファイル">
  <details>
   <summary>実装ファイル {{ files.length }} 件 · ブラウザーに保存して参照</summary>
   <p>チャット・レビューに列挙されたソースファイルを最大200件表示します。内容はコミットごとに{{ fileTTL / 3600000 }}時間保存します（1ファイル最大100 KiB・合計最大10 MiB）。未取得のファイルはGitHubから読み込みます。</p>
   <div class="file-actions"><button class="plain-button" :disabled="saving || !!pending" @click="saveAll">{{ saving ? `保存中 ${completed} / ${files.length}` : '一覧のファイルをまとめて保存' }}</button><button class="plain-button" :disabled="saving || !!pending" @click="clear">保存内容を削除</button></div>
   <p v-if="storageError" role="alert">{{ storageError }}</p>
   <ul class="file-list"><li v-for="file in files" :key="fileKey(file)">
    <button :disabled="saving || !!pending" @click="fetchFile(file)">{{ file.path }} <small>{{ file.commit.slice(0, 7) }} · {{ pending === fileKey(file) ? '取得中…' : cached(file) ? '保存済み・開く' : '保存して開く' }}</small></button>
    <p v-if="errors[fileKey(file)]" role="alert">{{ errors[fileKey(file)] }}</p>
   </li></ul>
  </details>
 </section>
</template>

<style scoped>
.source-files{margin:18px 0;border:1px solid #dce4d1;border-radius:8px;padding:16px;font-size:11px;line-height:1.8;color:#59644f}.source-files summary{cursor:pointer;font-weight:600}.source-files p{margin:8px 0;overflow-wrap:anywhere}.file-actions{display:flex;gap:15px;flex-wrap:wrap;margin:12px 0}.file-list{list-style:none;padding:0;max-height:300px;overflow:auto}.file-list li{border-top:1px solid #e6ebdd;padding:6px 0}.file-list button{display:block;width:100%;text-align:left;background:none;border:0;color:#416b34;overflow-wrap:anywhere}.file-list small{display:block;color:#7e8f6b}.file-list p[role=alert]{color:#9b6234}
</style>
