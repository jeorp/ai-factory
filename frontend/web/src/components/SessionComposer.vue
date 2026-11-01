<script setup lang="ts">
import { computed } from 'vue';
import type { Session } from '../types';
import { errorMessage } from '../lib/errorMessages';
import { processedFileCount } from '../lib/reviewView';
import { canResume, type ComposerMode } from '../lib/sessionView';
import Icon from './Icon.vue';

const props = defineProps<{ session: Session; running: boolean; connected: boolean; reconnecting: boolean; canRun: boolean; canSend: boolean }>();
const text = defineModel<string>('text', { required: true });
const mode = defineModel<ComposerMode>('mode', { required: true });
const fixPull = defineModel<string>('fixPull', { required: true });
const emit = defineEmits<{ send: []; stop: []; resume: [] }>();

const placeholders: Record<ComposerMode, string> = {
 message: '質問や「このPRをレビューして」「〜を実装してPRを作って」など…',
 implement: '実装してほしい変更（例: ログイン画面にエラー表示を追加して）…',
 instruction: '次の区切りで反映する追加指示…',
};
const resumable = computed(() => canResume(props.session));

function keydown(event: KeyboardEvent) {
 if (event.key === 'Enter' && !event.shiftKey && !event.isComposing) { event.preventDefault(); emit('send'); }
}
</script>

<template>
 <div class="composer-area">
  <div v-if="reconnecting" class="connection-notice">再接続中です。保存した会話を復元しています。</div>
  <div v-if="session.state === 'failed'" class="connection-notice" role="alert">{{ errorMessage(session.limitations.at(-1) || 'run_failed') }}</div>
  <div v-if="session.state === 'user_wait'" class="connection-notice" role="status">今回の実行時間または利用量の上限に達したため一時停止しました。途中結果は保存済みです。<template v-if="session.review">{{ processedFileCount(session.review) }} / {{ session.review.totalFiles }} ファイルを処理済みです。</template>「保存済みの状態から再開」で残りを続行できます。</div>
  <div v-if="resumable" class="resume-row"><span>{{ session.state === 'done' ? '中断したレビューを保存した区切りから続行できます。' : '保存した区切りから続行できます。' }}</span><button class="plain-button" :disabled="running || !canRun" @click="$emit('resume')"><Icon name="refresh" :size="13" />保存済みの状態から再開</button></div>
  <div class="composer">
   <textarea v-model="text" :placeholder="placeholders[mode]" aria-label="メッセージ" rows="2" maxlength="4096" @keydown="keydown" />
   <div class="composer-toolbar">
    <div>
     <select v-model="mode" aria-label="メッセージの種類"><option value="message">チャット</option><option value="implement">実装を依頼</option><option value="instruction">追加指示</option></select>
     <input v-if="mode === 'implement'" v-model="fixPull" class="fix-pull" inputmode="numeric" pattern="[0-9]*" maxlength="9" placeholder="修正するPR番号（任意）" aria-label="修正を追加するPR番号" title="AIファクトリーが作成したPRの番号を入れると、新しいPRを作らずそのPRのブランチに修正を追加します" />
     <span class="composer-model">{{ session.model }}</span>
    </div>
    <button v-if="running" class="stop-button" :disabled="session.state === 'stopping'" @click="$emit('stop')"><Icon name="stop" :size="13" />停止</button>
    <button class="send-button" :disabled="!canSend" aria-label="送信" @click="$emit('send')"><Icon name="arrow" :size="17" /></button>
   </div>
  </div>
  <div class="composer-caption"><span><span class="status-dot" :class="{ offline: !connected }"></span>{{ connected ? '接続中' : '未接続' }} · 書き込みは確認後のみ<span v-if="session.activePullRequest"> · 対象 PR #{{ session.activePullRequest }}</span></span><span>Enterで送信</span></div>
 </div>
</template>
