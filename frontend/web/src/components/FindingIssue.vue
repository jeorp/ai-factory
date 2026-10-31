<script setup lang="ts">
import { computed, ref } from 'vue';
import { issueDraft } from '../lib/issueDraft';
import type { Finding } from '../types';

const props = defineProps<{ finding: Finding; repository: string; pullNumber: number; sourceUrl: string }>();
const draft = computed(() => issueDraft(props.finding, props.repository, props.pullNumber, props.sourceUrl));
const copyStatus = ref('');
async function copyBody() {
 try { await navigator.clipboard.writeText(draft.value.body); copyStatus.value = '本文をコピーしました。'; }
 catch { copyStatus.value = 'コピーできませんでした。下の本文を選択してコピーしてください。'; }
}
</script>

<template>
 <div class="issue-action">
  <a v-if="draft.fullUrl" class="issue-button" :href="draft.fullUrl" target="_blank" rel="noopener noreferrer">GitHub で Issue を作成 ↗</a>
  <details v-else>
   <summary>GitHub で Issue を作成 ↗</summary>
   <p>本文が長いため、コピーして GitHub の作成画面に貼り付けてください。</p>
   <button type="button" @click="copyBody">本文をコピー</button>
   <span role="status">{{ copyStatus }}</span>
   <textarea :value="draft.body" readonly aria-label="Issue の本文" rows="8" @focus="($event.target as HTMLTextAreaElement).select()" />
   <a class="issue-button" :href="draft.fallbackUrl" target="_blank" rel="noopener noreferrer">GitHub の作成画面を開く ↗</a>
  </details>
 </div>
</template>

<style scoped>
.issue-action{margin-top:12px}.issue-button,.issue-action button{display:inline-flex;align-items:center;padding:8px 11px;border:1px solid #ccd9c8;border-radius:5px;background:#f6faf3;color:#315b3d;font-size:11px;text-decoration:none}.issue-button:hover,.issue-action button:hover{background:#eaf2e6}.issue-action summary{color:#315b3d;font-size:11px}.issue-action textarea{display:block;width:100%;margin:10px 0;padding:10px;resize:vertical;font:12px/1.6 ui-monospace,SFMono-Regular,Consolas,monospace}.issue-action span{margin-left:8px;font-size:10px}
</style>
