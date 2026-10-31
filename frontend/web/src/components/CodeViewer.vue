<script setup lang="ts">
import { computed, onUnmounted, ref, watch } from 'vue';
import { highlightCode } from '../lib/codeHighlight';
import { fileKey, type SourceFile } from '../lib/sourceFiles';
import Icon from './Icon.vue';
import 'highlight.js/styles/github.css';

const props = defineProps<{ file: SourceFile; panel?: boolean }>();
defineEmits<{ close: [] }>();
const code = computed(() => highlightCode(props.file.path, props.file.content));
const link = computed(() => `https://github.com/${props.file.repository.split('/').map(encodeURIComponent).join('/')}/blob/${encodeURIComponent(props.file.commit)}/${props.file.path.split('/').map(encodeURIComponent).join('/')}`);
const size = computed(() => code.value.bytes < 1024 ? `${code.value.bytes} Bytes` : `${(code.value.bytes / 1024).toFixed(1)} KiB`);
const copied = ref(false); const copyError = ref(''); let timer: ReturnType<typeof setTimeout> | undefined; let active = true;
watch(() => props.file, () => { copied.value = false; copyError.value = ''; clearTimeout(timer); });
async function copy() {
 const key = fileKey(props.file); const content = props.file.content; copyError.value = '';
 try {
  await navigator.clipboard.writeText(content);
  if (!active || fileKey(props.file) !== key) return;
  copied.value = true; clearTimeout(timer); timer = setTimeout(() => { copied.value = false; }, 2000);
 } catch { if (active && fileKey(props.file) === key) copyError.value = 'コピーできませんでした。コードを選択してコピーしてください。'; }
}
onUnmounted(() => { active = false; clearTimeout(timer); });
</script>

<template>
 <section class="code-viewer" :class="{ 'is-panel': panel }" :aria-label="`${file.path} のコード`">
  <header class="code-header">
   <div class="code-path"><Icon name="code" :size="16" /><strong>{{ file.path }}</strong></div>
   <div class="code-actions">
    <button type="button" :aria-label="`${file.path} のコードをコピー`" @click="copy"><Icon :name="copied ? 'check' : 'copy'" :size="14" />{{ copied ? 'コピー済み' : 'コピー' }}</button>
    <a :href="link" target="_blank" rel="noopener noreferrer" aria-label="このコミットのファイルをGitHubで開く"><Icon name="external" :size="14" />GitHub</a>
    <button type="button" aria-label="コード表示を閉じる" @click="$emit('close')">閉じる</button>
   </div>
  </header>
  <div class="code-meta"><span>{{ code.language }}<span class="code-separator">·</span>{{ code.lines }} 行<span class="code-separator">·</span>{{ size }}</span><code :title="file.commit">{{ file.commit.slice(0, 12) }}</code></div>
  <p v-if="copyError" class="copy-error" role="alert">{{ copyError }}</p>
  <p v-if="!code.lines" class="empty-file">空のファイルです。</p>
  <div v-else class="code-scroll" tabindex="0" role="region" :aria-label="`${file.path} のコード本文。左右にスクロールできます`">
   <div class="code-grid">
    <pre class="code-gutter" aria-hidden="true">{{ code.numbers }}</pre>
    <!-- Only highlight.js output or explicitly escaped text enters this HTML sink. -->
    <pre class="code-text"><code class="hljs" v-html="code.html" /></pre>
   </div>
  </div>
  <span class="sr-only" role="status">{{ copied ? 'コードをコピーしました。' : '' }}</span>
 </section>
</template>

<style scoped>
.code-viewer{margin-top:16px;min-width:0;border:1px solid #d1d9e0;border-radius:8px;overflow:hidden;background:#fff;color:#1f2328;font-size:12px;line-height:1.5}.code-header{display:flex;align-items:center;justify-content:space-between;gap:12px;padding:12px 14px;background:#f6f8fa;border-bottom:1px solid #d1d9e0}.code-path{display:flex;align-items:center;gap:8px;min-width:0}.code-path svg{flex-shrink:0;color:#59636e}.code-path strong{overflow-wrap:anywhere;font-size:12px;font-weight:600;font-family:ui-monospace,SFMono-Regular,Consolas,monospace}.code-actions{display:flex;align-items:center;gap:6px;flex-shrink:0}.code-actions button,.code-actions a{display:inline-flex;align-items:center;justify-content:center;gap:5px;min-height:28px;padding:4px 8px;border:1px solid #d1d9e0;border-radius:6px;background:#fff;color:#59636e;font-size:11px;text-decoration:none;white-space:nowrap}.code-actions button:hover,.code-actions a:hover{background:#eef1f4;color:#1f2328}.code-meta{display:flex;justify-content:space-between;align-items:center;flex-wrap:wrap;gap:8px;padding:9px 14px;color:#59636e;font-size:11px;border-bottom:1px solid #eaeef2}.code-meta code{font-family:ui-monospace,SFMono-Regular,Consolas,monospace;font-size:11px}.code-separator{padding:0 8px;color:#8c959f}.code-scroll{max-height:480px;overflow:auto;background:#fff;scrollbar-gutter:stable;isolation:isolate}.code-scroll:focus-visible{outline:2px solid #0969da;outline-offset:-2px}.code-grid{display:grid;grid-template-columns:max-content minmax(0,1fr);min-width:100%;width:max-content;align-items:stretch}.code-grid pre{margin:0;border:0;border-radius:0;font:12px/22px ui-monospace,SFMono-Regular,Consolas,"Liberation Mono",monospace;tab-size:4;white-space:pre;letter-spacing:0}.code-gutter{position:sticky;left:0;z-index:1;min-width:48px;padding:14px 12px 14px 16px;text-align:right;user-select:none;color:#8c959f;background:#fff;border-right:1px solid #eaeef2!important}.code-text{padding:14px 20px 14px 14px;color:#1f2328;background:#fff}.code-text>code.hljs{display:block;margin:0;padding:0;overflow:visible;font:inherit;background:transparent;color:inherit}.empty-file,.copy-error{padding:16px!important;margin:0!important;color:#59636e}.copy-error{color:#9a6700;background:#fff8c5}.sr-only{position:absolute;width:1px;height:1px;padding:0;overflow:hidden;clip-path:inset(50%);white-space:nowrap}@media(max-width:700px){.code-header{align-items:flex-start;flex-direction:column;gap:10px}.code-actions{align-self:flex-end}.code-grid pre{font-size:11px;line-height:21px}.code-gutter{min-width:36px;padding-left:10px;padding-right:10px}.code-text{padding-left:12px;padding-right:14px}}
</style>

<style scoped>
.code-viewer.is-panel{margin:0;height:100%;display:flex;flex-direction:column;border:0;border-radius:0}.is-panel .code-header{flex-shrink:0;flex-wrap:wrap}.is-panel .code-path{flex:1 1 240px}.is-panel .code-actions{margin-left:auto}.is-panel .code-meta{flex-shrink:0}.is-panel .code-scroll{flex:1;min-height:0;max-height:none}
@media(max-width:700px){.is-panel .code-path{flex:0 1 auto}}
</style>
