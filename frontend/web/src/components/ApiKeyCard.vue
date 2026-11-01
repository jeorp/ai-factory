<script setup lang="ts">
import type { Config } from '../types';
import type { ApiKeyState } from '../composables/useApiKey';
import Icon from './Icon.vue';

defineProps<{ provider: Config['aiProvider']; keys: ApiKeyState }>();
</script>

<template>
 <div class="key-card">
  <div class="key-heading"><Icon name="key" :size="17" /><strong>{{ provider === 'vertex' ? 'Vertex AIのアクセストークンを接続' : `${provider === 'gemini' ? 'Gemini' : 'OpenAI'} APIキーを接続` }}</strong><span>利用は最大1時間</span></div>
  <template v-if="provider === 'vertex'">
   <p>回答の生成時に、非公開コードを含む取得情報をGoogle Cloud（Vertex AI）経由でClaudeへ送信します。利用料はサーバーに設定したGCPプロジェクトに発生します。</p>
   <p>ターミナルで <code>gcloud auth print-access-token</code> を実行し、表示されたトークンを入力してください。トークンの有効期限は約1時間です。</p>
  </template>
  <p v-else-if="provider === 'gemini'">回答の生成時に非公開コードを含む取得情報をGoogleへ送信します。無料枠では送信内容がGoogleのサービス改善に使われる場合があります。利用枠や料金はGoogleのプロジェクト設定によります。</p>
  <p v-else>回答の生成時に、非公開コードを含む取得情報をOpenAIへ送信します。API利用料はご自身のOpenAIアカウントに発生します。ChatGPTの契約とは別です。</p>
  <label class="consent"><input v-model="keys.consent" type="checkbox" />送信内容と費用について確認しました</label>
  <div v-if="keys.hasSaved" role="group" aria-label="APIキーの選択">
   <label class="consent"><input v-model="keys.choice" type="radio" value="saved" name="key-choice" />保存済みのAPIキーを使う</label>
   <label class="consent"><input v-model="keys.choice" type="radio" value="new" name="key-choice" />新しいAPIキーを入力する</label>
   <button class="plain-button" type="button" @click="keys.forgetSaved">保存済みのキーを削除</button>
  </div>
  <form @submit.prevent="keys.apply">
   <template v-if="keys.choice === 'new'">
    <div class="key-form"><input v-model="keys.draft" type="password" autocomplete="off" spellcheck="false" :placeholder="provider === 'vertex' ? 'アクセストークン（ya29.…）を入力' : 'APIキーを入力'" aria-label="AI APIキー" maxlength="512" /></div>
    <label class="consent"><input v-model="keys.remember" type="checkbox" />APIキーをこのブラウザに保存する{{ keys.hasSaved ? '（保存済みのキーを上書き）' : '' }}</label>
    <p>保存したキーは次のセッションでも選択できます。共有端末では保存を避けてください。</p>
   </template>
   <button class="primary-button" :disabled="!keys.consent || keys.pending">接続する</button>
  </form>
 </div>
</template>
