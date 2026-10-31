<script setup lang="ts">
import { computed, onUnmounted, ref } from 'vue';
import { api } from '../lib/auth';
import type { Review } from '../types';

const props = defineProps<{ sessionId: string; review: Review }>();
type Diff = { path: string; previousPath: string; status: string; patch: string; base: string; head: string };
const files = computed(() => props.review.steps.flatMap(step => step.files).filter(file => file.path));
const selected = ref<number>(); const diffs = ref<Record<number, Diff>>({}); const pending = ref<number>(); const error = ref('');
const controller = new AbortController(); let active = true;
async function open(index: number) {
 if (selected.value === index) { selected.value = undefined; return; }
 selected.value = index; error.value = '';
 if (diffs.value[index]) return;
 pending.value = index;
 try {
  const diff = await api<Diff>(`/sessions/${encodeURIComponent(props.sessionId)}/review-diff?index=${index}`, { signal: controller.signal });
  if (!active) return;
  if (diff.base !== props.review.base || diff.head !== props.review.head) throw new Error('review_diff_snapshot_unavailable');
  diffs.value[index] = diff;
 } catch (e) {
  if (active && selected.value === index) error.value = e instanceof Error && e.message === 'review_diff_snapshot_unavailable' ? 'このレビュー時点の差分を取得できません。PRが更新された可能性があります。' : '差分を取得できませんでした。権限やファイルの状態を確認してください。';
 } finally { if (active && pending.value === index) pending.value = undefined; }
}
onUnmounted(() => { active = false; controller.abort(); });
</script>

<template>
 <section v-if="files.length" class="diffs" aria-label="ベースからの差分">
  <h3>ベースからの差分</h3>
  <p>レビュー対象の Base → Head の変更を表示します。GitHub が差分を返さないファイルや、途中までの差分もあります。</p>
  <div v-for="file in files" :key="file.index" class="diff-file">
   <button class="diff-toggle" type="button" :aria-expanded="selected === file.index" @click="open(file.index)"><span>{{ file.path }}</span><small>{{ file.status }} · {{ selected === file.index ? '閉じる' : '差分を見る' }}</small></button>
   <template v-if="selected === file.index">
    <p v-if="pending === file.index" role="status">差分を取得しています…</p>
    <p v-else-if="error" role="alert">{{ error }}</p>
    <template v-else-if="diffs[file.index]">
     <p v-if="diffs[file.index].previousPath">変更前: {{ diffs[file.index].previousPath }}</p>
     <p v-if="!diffs[file.index].patch">このファイルの差分は取得できませんでした。</p>
     <div v-else class="patch-scroll" tabindex="0" role="region" :aria-label="`${file.path} の差分`"><pre><span v-for="(line, i) in diffs[file.index].patch.split('\n')" :key="i" :class="{ add: line.startsWith('+'), remove: line.startsWith('-'), hunk: line.startsWith('@@') }">{{ line }}</span></pre></div>
    </template>
   </template>
  </div>
 </section>
</template>

<style scoped>
.diffs{margin-top:20px}.diffs>p{font-size:12px;color:#63766b}.diff-file{border-top:1px solid #e2e8e4}.diff-toggle{display:flex;justify-content:space-between;gap:12px;width:100%;padding:11px 0;text-align:left;border:0;background:none;color:#315e31;cursor:pointer}.diff-toggle span{overflow-wrap:anywhere;font-family:ui-monospace,SFMono-Regular,Consolas,monospace}.diff-toggle small{flex-shrink:0;color:#63766b}.patch-scroll{max-height:420px;overflow:auto;border:1px solid #d1d9e0;border-radius:6px;background:#f6f8fa}.patch-scroll pre{margin:0;min-width:max-content;font:12px/1.7 ui-monospace,SFMono-Regular,Consolas,monospace;tab-size:4}.patch-scroll span{display:block;padding:0 12px;white-space:pre;min-height:1.7em}.patch-scroll .add{background:#dafbe1;color:#116329}.patch-scroll .remove{background:#ffebe9;color:#82071e}.patch-scroll .hunk{background:#ddf4ff;color:#0550ae}
</style>
