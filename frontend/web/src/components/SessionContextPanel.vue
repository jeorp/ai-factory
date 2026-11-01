<script setup lang="ts">
import { computed } from 'vue';
import type { Config, Session } from '../types';
import { estimateCost, formatCost, pricing } from '../lib/pricing';
import { limitationMessage } from '../lib/errorMessages';
import { safeGitHubLink } from '../lib/github';
import { inputLimitBytes, shortDate, type ReviewPolicy } from '../lib/sessionView';
import Icon from './Icon.vue';

const props = defineProps<{ session: Session; provider: Config['aiProvider']; reviewPolicy: ReviewPolicy }>();
const deleteId = defineModel<string>('deleteId', { required: true });
defineEmits<{ remove: [id: string] }>();

const inputTokens = computed(() => props.session.metrics.inputTokens || 0);
const outputTokens = computed(() => props.session.metrics.outputTokens || 0);
const cost = computed(() => estimateCost(props.provider, props.session.model, inputTokens.value, outputTokens.value));
const sessionPricing = computed(() => pricing(props.provider, props.session.model));
const inputLimit = computed(() => inputLimitBytes(props.session, props.reviewPolicy));
</script>

<template>
 <aside class="context-panel">
  <div class="context-title"><Icon name="grid" :size="16" /><h2>セッションの情報</h2></div>
  <div class="context-block"><h3>対象リポジトリ</h3><strong class="repo-name">{{ session.target.repository }}</strong><div class="commit"><Icon name="branch" :size="13" /><code>{{ session.target.commit.slice(0, 12) }}</code><span>固定</span></div><p>{{ session.review ? 'レビューは表示されたベース・ヘッドを固定して確認します。コメントは取得時点の情報です。' : 'ファイルはこのコミットを参照します。PR・Issueは取得時点の情報です。' }}</p></div>
  <div class="context-block">
   <h3>セッションの利用量</h3>
   <dl class="metrics"><div><dt>入力トークン</dt><dd>{{ inputTokens.toLocaleString() }}</dd></div><div><dt>出力トークン</dt><dd>{{ outputTokens.toLocaleString() }}</dd></div><div><dt>合計トークン</dt><dd>{{ (inputTokens + outputTokens).toLocaleString() }}</dd></div><div><dt>概算コスト（USD）</dt><dd>{{ cost === undefined ? '算出できません' : formatCost(cost) }}</dd></div></dl>
   <p v-if="sessionPricing">標準料金での概算です。キャッシュ割引や無料枠などは反映していません。<br /><a :href="sessionPricing.url" target="_blank" rel="noopener noreferrer">料金表を確認 ↗</a></p><p v-else>このモデルの料金情報がないため、コストは算出できません。</p>
  </div>
  <div class="context-block">
   <h3>コンテキストの利用状況</h3>
   <div class="meter-label"><span>最大入力データ量</span><strong>{{ (session.metrics.maxInputBytes / 1024).toFixed(1) }}<small> / {{ inputLimit / 1024 }} KiB</small></strong></div>
   <div class="meter"><span :style="{ width: Math.min(100, session.metrics.maxInputBytes / inputLimit * 100) + '%' }"></span></div>
   <dl class="metrics"><div><dt>GitHub取得回数</dt><dd>{{ session.metrics.toolCalls }} 回</dd></div><div><dt>取得サイズ</dt><dd>{{ (session.metrics.fetchedBytes / 1024).toFixed(1) }} KiB</dd></div><div><dt>重複取得の抑制</dt><dd>{{ session.metrics.cacheHits }} 回</dd></div><div><dt>直近の処理時間</dt><dd>{{ (session.metrics.durationMs / 1000).toFixed(1) }} 秒</dd></div><div><dt>プロセスのヒープ観測最大</dt><dd>{{ (session.metrics.heapPeakBytes / 1048576).toFixed(1) }} MiB</dd></div></dl>
   <p>{{ session.runKind === 'review' ? 'PRは最大200ファイルを計画し、関連する差分を最大10ファイルまとめて自動で確認します。中断した場合は保存した区切りから再開できます。' : '1質問あたり最大6回・5ファイル。取得結果は一時保持し、履歴全体はAIに渡しません。' }}</p>
   <p v-if="session.runKind === 'review'">入力は差分約{{ reviewPolicy.diffTokens.toLocaleString() }}・全体約{{ reviewPolicy.inputTokens.toLocaleString() }}トークンを目安に調整します（共通トークナイザーによる推定）。1回の継続実行は最大{{ reviewPolicy.maxMinutes }}分・AI{{ reviewPolicy.maxCalls }}回・入力{{ reviewPolicy.runInputTokens.toLocaleString() }}／出力{{ reviewPolicy.runOutputTokens.toLocaleString() }}トークン。停止・切断・上限到達まで自動で続けます。</p>
  </div>
  <div class="context-block"><h3>参照元<span class="source-count">{{ session.sources?.length || 0 }}</span></h3><p v-if="!session.sources?.length">取得した情報の参照元が表示されます。</p><a v-for="source in session.sources" :key="source.url" class="source-link" :href="safeGitHubLink(source.url)" target="_blank" rel="noopener noreferrer"><Icon name="link" :size="13" /><span>{{ source.label }}</span><Icon name="external" :size="12" /></a></div>
  <details v-if="session.summary" class="context-block"><summary>引き継ぎ用の要点</summary><p class="summary-text">{{ session.summary }}</p></details>
  <div v-if="!session.review && session.limitations?.length" class="context-block limitations"><h3>未確認の範囲・制限</h3><p v-for="(limitation, i) in session.limitations" :key="i">{{ limitationMessage(limitation) }}</p></div>
  <div class="retention"><Icon name="clock" :size="14" /><span>会話の保存期限 {{ shortDate(session.expiresAt) }}<br />会話はいつでも削除できます。</span></div>
  <button class="plain-button context-delete" @click="deleteId = session.id"><Icon name="trash" :size="12" />このセッションを削除</button>
  <div v-if="deleteId === session.id" class="delete-confirm">会話と結果を削除します。<button @click="$emit('remove', session.id)">削除する</button></div>
 </aside>
</template>
