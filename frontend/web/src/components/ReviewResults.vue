<script setup lang="ts">
import { computed } from 'vue';
import type { Review, Session } from '../types';
import { findingLink, priorityLabels, processedFileCount, reviewFileStatus, reviewPhaseLabel, reviewScopeNote, reviewStepStateLabel, unverifiedItems } from '../lib/reviewView';
import ReviewDiffs from './ReviewDiffs.vue';
import FindingEvidence from './FindingEvidence.vue';
import FindingIssue from './FindingIssue.vue';

const props = defineProps<{ session: Session; review: Review; demo: boolean; running: boolean; canRun: boolean }>();
defineEmits<{ investigate: [index: number] }>();

const items = computed(() => unverifiedItems(props.review));
const scopeNote = computed(() => reviewScopeNote(props.review));
const canCreateIssue = computed(() => !props.demo && props.session.target.kind === 'pull' && !!props.session.target.number);
</script>

<template>
 <details class="review-results">
  <summary>PRレビュー結果 · {{ review.title }} · 指摘 {{ review.findings.length }} 件</summary>
  <h2>{{ review.title }}</h2>
  <p class="review-commits">Base <code>{{ review.base.slice(0, 12) }}</code> → Head <code>{{ review.head.slice(0, 12) }}</code></p>
  <p>{{ reviewPhaseLabel(review.phase) }} · {{ processedFileCount(review) }} / {{ review.totalFiles }} ファイルの処理済み（取得失敗・対象外を含む）</p>
  <p v-if="scopeNote">対象範囲: {{ scopeNote }}。共通の設定・ドキュメントも確認します。</p>
  <details><summary>レビュー計画と処理状況</summary><div v-for="(step, i) in review.steps" :key="i" class="review-step"><strong>{{ i + 1 }}. {{ step.directory }} · {{ reviewStepStateLabel(step.state) }}</strong><ul><li v-for="file in step.files" :key="file.index">{{ file.path || `一覧取得失敗（${file.index + 1}番目）` }} · {{ reviewFileStatus(file) }}</li></ul><p v-if="step.summary">{{ step.summary }}</p></div></details>
  <ReviewDiffs :key="`${session.id}-${review.base}-${review.head}`" :session-id="session.id" :review="review" />
  <h3>指摘 {{ review.findings.length }} 件</h3>
  <p v-if="!review.findings.length">{{ review.phase === 'done' ? '確認した範囲では、保存された指摘はありません。' : '指摘は確認した区切りごとに表示されます。' }}</p>
  <article v-for="(finding, i) in review.findings" :key="i" class="review-finding">
   <h3><span class="finding-severity">{{ finding.severity }}</span> {{ finding.title }}</h3>
   <a :href="findingLink(session.target.repository, review, finding)" target="_blank" rel="noopener noreferrer">{{ finding.path }}{{ finding.line ? ':' + finding.line : '（行未特定）' }} · {{ finding.side === 'LEFT' ? '変更前' : '変更後' }}</a>
   <p><strong>問題:</strong> {{ finding.problem }}</p><p><strong>影響:</strong> {{ finding.impact }}</p><FindingEvidence :evidence="finding.evidence" /><p v-if="finding.suggestion"><strong>修正案:</strong> {{ finding.suggestion }}</p>
   <FindingIssue v-if="canCreateIssue" :finding="finding" :repository="session.target.repository" :pull-number="session.target.number" :source-url="findingLink(session.target.repository, review, finding)" />
  </article>
  <details v-if="review.unverified.length" open><summary>未確認の範囲・制限 {{ review.unverified.length }} 件</summary>
   <p>優先度はレビューへの影響の推定です。未評価の項目も残します。追加調査では関連情報を再確認します。</p><p v-if="items.length < review.unverified.length" role="status">未確認項目の優先度を確認しています。中断した場合は保存済みの状態から再開できます。</p>
   <ul class="unverified-list">
    <li v-for="entry in items" :key="entry.index">
     <p><strong class="priority-label">優先度 {{ priorityLabels[entry.priority?.level || 'unknown'] }}</strong> {{ entry.text }}</p>
     <p v-if="entry.priority?.reason" class="priority-reason">{{ entry.priority.reason }}</p>
     <button type="button" class="investigate-button" :disabled="running || !canRun" @click="$emit('investigate', entry.index)">{{ review.investigations?.[entry.index] ? 'もう一度調査' : '追加調査' }}</button>
     <span v-if="review.pendingInvestigation === entry.index + 1" class="investigation-status">{{ running ? '調査中' : '調査が中断されています。保存済みの状態から再開できます。' }}</span>
     <div v-if="review.investigations?.[entry.index]" class="investigation-result"><strong>追加調査の結果</strong><p>{{ review.investigations[entry.index] }}</p></div>
    </li>
   </ul>
  </details>
 </details>
</template>
