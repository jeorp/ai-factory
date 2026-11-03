<script setup lang="ts">
import { computed, ref, watch } from 'vue';
import type { Documentation } from '../types';
import { documentationAtCallLimit, documentationFileURL, documentationLimitations, documentationMarkdown } from '../lib/documentation';
import Icon from './Icon.vue';

const props = defineProps<{ documentation?: Documentation; running: boolean; canRun: boolean; sessionState: string; active: boolean; sessionId: string }>();
const emit = defineEmits<{ generate: [value: { text: string; scope: 'feature' | 'repository' }]; resume: [] }>();
const scope = ref<'feature' | 'repository'>('feature'); const request = ref(''); const notice = ref('');
watch(() => `${props.documentation?.scope || ''}\0${props.documentation?.request || ''}`, () => {
 if (props.documentation) { scope.value = props.documentation.scope; request.value = props.documentation.request; }
}, { immediate: true });
const result = computed(() => props.documentation?.result);
const limits = computed(() => props.documentation ? documentationLimitations(props.documentation) : []);
const researching = computed(() => props.documentation?.status === 'researching');
const callLimit = computed(() => !props.running && documentationAtCallLimit(props.documentation));
const generating = computed(() => researching.value && props.running && props.active);
const canResume = computed(() => researching.value && !callLimit.value && props.active && !props.running && ['failed', 'stopped', 'auth_wait', 'user_wait'].includes(props.sessionState));
function generate() {
 if (props.running || !props.canRun || (scope.value === 'feature' && !request.value.trim())) return;
 notice.value = ''; emit('generate', { text: request.value.trim(), scope: scope.value });
}
async function copy() {
 if (!props.documentation) return;
 try { await navigator.clipboard.writeText(documentationMarkdown(props.documentation)); notice.value = 'Markdownをコピーしました。'; }
 catch { notice.value = 'コピーできませんでした。「Markdownで保存」をお使いください。'; }
}
function download() {
 if (!props.documentation) return;
 const url = URL.createObjectURL(new Blob([documentationMarkdown(props.documentation)], { type: 'text/markdown;charset=utf-8' }));
 const a = document.createElement('a'); a.href = url; a.download = `implementation-guide-${props.documentation.commit.slice(0, 12)}.md`;
 a.click(); setTimeout(() => URL.revokeObjectURL(url), 1000);
}
</script>

<template>
 <section class="documentation-panel" aria-label="実装理解の資料">
  <header class="documentation-header"><div class="documentation-symbol"><Icon name="document" :size="21" /></div><div><span class="documentation-eyebrow">IMPLEMENTATION GUIDE</span><h2>実装を、読み解く。</h2><p>機能の入口から処理のつながりまで、コードを辿るための資料。</p></div></header>
  <details class="documentation-form" :open="!result">
   <summary>{{ result ? '対象を指定して資料を作り直す' : '理解したい対象を選ぶ' }}</summary>
   <form @submit.prevent="generate">
    <fieldset :disabled="running"><legend>資料の範囲</legend><label><input v-model="scope" type="radio" value="feature" :name="`documentation-scope-${sessionId}`" />機能・画面</label><label><input v-model="scope" type="radio" value="repository" :name="`documentation-scope-${sessionId}`" />リポジトリ全体</label></fieldset>
    <label class="documentation-request"><span>{{ scope === 'feature' ? '理解したい機能・画面' : '特に知りたいこと（任意）' }}</span><textarea v-model="request" :disabled="running" :required="scope === 'feature'" :placeholder="scope === 'feature' ? '例：ログイン機能。画面操作から認証・セッション保存まで知りたい' : '例：全体構成と、主要機能の処理のつながりを知りたい'" rows="3" maxlength="1300" /></label>
    <div class="documentation-form-bottom"><p>処理の流れ・関連コード・読む順番・変更時の注意点をまとめます。</p><button class="primary-button" :disabled="running || !canRun || (scope === 'feature' && !request.trim())"><Icon name="document" :size="15" />{{ generating ? '資料を作成中…' : '資料を作成' }}</button></div>
    <p v-if="!canRun && !running" class="documentation-help">接続とAIのAPIキーを確認すると作成できます。</p>
   </form>
  </details>
  <div v-if="researching" class="documentation-research" role="status"><strong>{{ callLimit ? '資料作成の上限に達しました' : generating ? '関連コードを調べています' : '資料作成は途中です' }}</strong><p>{{ callLimit ? 'AIの呼び出し上限に達しました。対象を絞って資料を作り直してください。' : generating ? '処理の入口と呼び出し先を確認し、資料にまとめます。' : '取得済みの調査は保存されています。' }}</p><button v-if="canResume" class="plain-button" :disabled="!canRun" @click="emit('resume')">保存済みの調査から再開</button><p v-else-if="!active && !running">同じ対象で再度作成すると、調査を最初からやり直します。</p></div>
  <article v-if="result && documentation" class="documentation-result">
   <div class="documentation-meta"><span>{{ documentation.scope === 'feature' ? '機能・画面の実装ガイド' : 'リポジトリ全体の実装ガイド' }}</span><code>{{ documentation.commit.slice(0, 12) }}</code></div>
   <h3>{{ result.title }}</h3><p class="documentation-lead">{{ result.summary }}</p>
   <div class="documentation-export"><button class="plain-button" @click="copy"><Icon name="copy" :size="13" />コピー</button><button class="plain-button" @click="download"><Icon name="download" :size="13" />Markdownで保存</button></div><p v-if="notice" role="status" class="documentation-help">{{ notice }}</p>
   <nav aria-label="資料の目次" class="documentation-toc"><a v-for="(section, index) in result.sections" :key="index" :href="`#guide-${sessionId}-${index}`"><span>{{ String(index + 1).padStart(2, '0') }}</span>{{ section.heading }}</a></nav>
   <section v-for="(section, index) in result.sections" :id="`guide-${sessionId}-${index}`" :key="index" class="documentation-section"><h4><span>{{ String(index + 1).padStart(2, '0') }}</span>{{ section.heading }}</h4><p>{{ section.body }}</p><div v-if="section.paths.length" class="documentation-paths"><span>参照コード</span><template v-for="path in section.paths" :key="path"><a v-if="documentationFileURL(documentation, path) !== '#'" :href="documentationFileURL(documentation, path)" target="_blank" rel="noopener noreferrer"><Icon name="code" :size="13" />{{ path }}<Icon name="external" :size="11" /></a></template></div></section>
   <aside v-if="limits.length" class="documentation-limits"><h4>未確認事項・調査の範囲</h4><ul><li v-for="(limit, index) in limits" :key="index">{{ limit }}</li></ul></aside>
  </article>
 </section>
</template>

<style scoped>
.documentation-panel{margin:22px 0;border:1px solid #dbe5d4;border-radius:12px;background:#fff;overflow:hidden;color:#42523d;font-size:12px;line-height:1.8;overflow-wrap:anywhere}.documentation-header{display:flex;gap:13px;align-items:flex-start;padding:23px 24px;background:linear-gradient(125deg,#f1f6eb,#fbfcf8)}.documentation-symbol{display:grid;place-items:center;flex-shrink:0;width:38px;height:42px;border:1px solid #d9e4ce;background:#fff;border-radius:8px;color:#60814b}.documentation-eyebrow{font-size:8px;letter-spacing:1.7px;color:#788b69}.documentation-header h2{font-size:20px;font-weight:550;margin:2px 0 5px;letter-spacing:1px}.documentation-header p{font-size:11px;color:#79866d;margin:0}.documentation-form{padding:15px 24px;border-top:1px solid #e8eddf}.documentation-form summary{font-size:11px;color:#668256;cursor:pointer}.documentation-form form{margin-top:14px}.documentation-form fieldset{border:0;padding:0;display:flex;gap:20px;margin:0 0 14px}.documentation-form legend{font-size:10px;color:#74866a;margin-bottom:7px}.documentation-form fieldset label{display:flex;gap:6px;align-items:center;font-size:12px}.documentation-form input{accent-color:#4c723a}.documentation-request{display:grid;gap:7px;font-size:11px}.documentation-request textarea{resize:vertical;min-height:80px;border:1px solid #dae3d2;border-radius:6px;padding:10px 12px;font:inherit;font-size:12px;line-height:1.8;background:#fdfefb;color:#42523d;width:100%}.documentation-form-bottom{display:flex;align-items:center;gap:16px;justify-content:space-between;margin-top:12px}.documentation-form-bottom p{font-size:10px;color:#839177;margin:0}.documentation-form-bottom button{font-size:11px;white-space:nowrap;padding:9px 13px;flex-shrink:0}.documentation-help{font-size:10px;color:#76866c;margin:10px 0 0}.documentation-research{background:#f7f9f2;margin:0 24px 20px;padding:14px 16px;border-radius:6px}.documentation-research strong{font-size:12px;font-weight:550}.documentation-research p{font-size:11px;color:#79866d;margin:3px 0}.documentation-result{padding:24px}.documentation-meta{display:flex;align-items:center;gap:12px;justify-content:space-between;color:#7b8c70;font-size:10px}.documentation-meta code{font-size:10px}.documentation-result h3{font-size:22px;font-weight:550;line-height:1.6;margin:11px 0}.documentation-lead{white-space:pre-wrap;color:#748269;margin:0}.documentation-export{display:flex;justify-content:flex-end;gap:15px;margin:16px 0}.documentation-toc{display:grid;grid-template-columns:1fr 1fr;gap:8px 18px;background:#f7f9f3;padding:16px;border-radius:7px}.documentation-toc a{font-size:11px;color:#617a53;text-decoration:none}.documentation-toc span{margin-right:7px;color:#98aa8b;font-size:10px}.documentation-section{padding:23px 0;border-bottom:1px solid #e9eddf;scroll-margin-top:15px}.documentation-section h4{font-size:14px;font-weight:550;margin:0 0 12px;display:flex;gap:10px;align-items:baseline}.documentation-section h4 span{font-size:10px;color:#91a47e;font-weight:400}.documentation-section>p{white-space:pre-wrap;margin:0;line-height:2;color:#65745b}.documentation-paths{display:flex;gap:6px;flex-wrap:wrap;margin-top:13px;align-items:center}.documentation-paths>span{font-size:9px;color:#91a082;margin-right:3px}.documentation-paths a{display:inline-flex;gap:5px;align-items:center;max-width:100%;font-size:10px;color:#5a7949;background:#f1f6eb;padding:4px 7px;border-radius:4px;text-decoration:none}.documentation-paths a:hover{text-decoration:underline}.documentation-paths svg{flex-shrink:0}.documentation-limits{margin-top:22px;border-left:2px solid #c8bb93;padding:1px 14px;color:#938369;background:#fcfaf5}.documentation-limits h4{font-size:12px;font-weight:550;margin:10px 0 6px}.documentation-limits ul{padding-left:17px;font-size:11px;margin:8px 0 12px}.documentation-limits li+li{margin-top:6px}@media(max-width:650px){.documentation-header,.documentation-result{padding:18px}.documentation-form{padding:13px 18px}.documentation-header h2{font-size:18px}.documentation-form-bottom{align-items:stretch;flex-direction:column;gap:10px}.documentation-toc{grid-template-columns:1fr}.documentation-result h3{font-size:18px}.documentation-research{margin:0 18px 18px}.documentation-paths a{word-break:break-all}}
</style>
