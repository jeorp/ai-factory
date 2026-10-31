<script setup lang="ts">
import { computed } from 'vue';

const props = defineProps<{ evidence: string }>();
// Remove a surrounding Markdown fence only when it encloses the entire excerpt.
const excerpt = computed(() => {
 const text = props.evidence.replace(/\r\n/g, '\n');
 const fenced = text.match(/^```[^\n]*\n([\s\S]*?)\n```\s*$/);
 return fenced ? fenced[1] : text;
});
const isCode = computed(() => excerpt.value.includes('\n') || /^```/.test(props.evidence));
const lines = computed(() => excerpt.value.split('\n'));
</script>

<template>
 <div class="finding-evidence">
  <strong>根拠:</strong>
  <div v-if="isCode" class="evidence-scroll" tabindex="0" role="region" aria-label="根拠のコード。左右にスクロールできます">
   <pre><span v-for="(line, i) in lines" :key="i" :class="{ add: line.startsWith('+'), remove: line.startsWith('-'), hunk: line.startsWith('@@') }">{{ line || ' ' }}</span></pre>
  </div>
  <span v-else class="evidence-text">{{ excerpt }}</span>
 </div>
</template>

<style scoped>
.finding-evidence{margin:8px 0}.evidence-text{margin-left:4px;overflow-wrap:anywhere}.evidence-scroll{margin-top:6px;max-height:320px;overflow:auto;border:1px solid #d1d9e0;border-radius:6px;background:#f6f8fa;color:#1f2328}.evidence-scroll:focus-visible{outline:2px solid #0969da;outline-offset:-2px}.evidence-scroll pre{margin:0;min-width:max-content;font:12px/1.7 ui-monospace,SFMono-Regular,Consolas,monospace;tab-size:4}.evidence-scroll span{display:block;min-height:1.7em;padding:0 12px;white-space:pre}.evidence-scroll .add{background:#dafbe1;color:#116329}.evidence-scroll .remove{background:#ffebe9;color:#82071e}.evidence-scroll .hunk{background:#ddf4ff;color:#0550ae}
</style>
