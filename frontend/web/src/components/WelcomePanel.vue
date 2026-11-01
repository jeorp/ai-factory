<script setup lang="ts">
import type { Config, Repository } from '../types';
import Icon from './Icon.vue';

defineProps<{ login: string; config: Config; repositories: Repository[]; loading: boolean; actionPending: boolean; target: string }>();
const repositoryChoice = defineModel<string>('repositoryChoice', { required: true });
const manualTarget = defineModel<string>('manualTarget', { required: true });
const model = defineModel<string>('model', { required: true });
defineEmits<{ signIn: []; create: []; refreshRepositories: [] }>();
</script>

<template>
 <div class="welcome-scroll">
  <div class="welcome">
   <div class="eyebrow"><span class="green-line"></span> YOUR REPOSITORY, IN CONTEXT</div>
   <h1>コードを読み解く、<br /><span>次の会話をはじめよう。</span></h1>
   <p class="welcome-description">リポジトリの構成も、PRの背景も、気になるIssueも。<br />GitHubの情報を必要な分だけ集めて、根拠とともに整理します。</p>
   <div class="setup-card">
    <div class="card-heading"><div class="number-label">01</div><div><h2>{{ login ? '調べたいリポジトリを選択' : 'GitHubとつなげて、はじめる' }}</h2><p>{{ login ? '許可したリポジトリ・PR・Issueを参照できます。' : 'GitHub Appで、参照するリポジトリを選べます。' }}</p></div><Icon name="branch" :size="22" /></div>
    <div v-if="!login" class="login-area"><button class="primary-button" :disabled="loading || actionPending" @click="$emit('signIn')"><Icon name="code" :size="18" />{{ config.demo ? '開発用ログイン' : 'GitHubでログイン' }}<Icon name="chevron" :size="16" /></button><span>コードの変更は、確認後に新しいブランチのDraft PRとしてだけ作成します。</span></div>
    <form v-else @submit.prevent="$emit('create')">
     <label for="repository">リポジトリ</label>
     <select id="repository" v-model="repositoryChoice" class="repository-select" required><option value="" disabled>リポジトリを選択してください</option><option v-for="r in repositories" :key="r.full_name" :value="r.full_name">{{ r.full_name }}</option><option value="__manual__">リポジトリ・PR・Issueを直接入力</option></select>
     <div v-if="repositoryChoice === '__manual__'" class="manual-target"><label for="target">リポジトリ / PR / Issue</label><div class="input-with-icon"><Icon name="link" :size="17" /><input id="target" v-model="manualTarget" placeholder="owner/repository または https://github.com/…" required maxlength="1024" /></div></div>
     <div class="repository-actions"><span>{{ repositories.length }} 件のリポジトリに接続</span><button class="plain-button" type="button" @click="$emit('refreshRepositories')"><Icon name="refresh" :size="12" />再取得</button><a v-if="!config.demo" :href="config.installUrl" target="_blank" rel="noopener noreferrer">アクセスを設定<Icon name="external" :size="12" /></a></div>
     <div class="form-bottom"><div class="model-select"><label for="model">AIモデル</label><select id="model" v-model="model"><option v-for="m in config.models" :key="m" :value="m">{{ m.replace(/^vertex\//, '') }}{{ ({ vertex: '（Vertex AI）', anthropic: '（Anthropic API）' } as Record<string, string>)[config.modelProviders?.[m] || ''] || '' }}</option></select></div><button class="primary-button" :disabled="actionPending || !target"><span>セッションを作成</span><Icon name="chevron" :size="16" /></button></div>
    </form>
   </div>
   <div class="feature-grid"><div><Icon name="link" :size="20" /><h3>根拠から、わかる。</h3><p>参照元と未確認の範囲を<br />一緒に確認できます。</p></div><div><Icon name="grid" :size="20" /><h3>必要な情報だけ。</h3><p>会話と取得情報を分け、<br />コンテキストを小さく保ちます。</p></div><div><Icon name="key" :size="20" /><h3>キーの保存を選べる。</h3><p>自分のAI APIキーを使用。<br />ブラウザへの保存は任意です。</p></div></div>
   <div class="welcome-footer"><span class="small-orbit">✳</span> 小さく読み取り、理解を深める。<span>PHASE 02 — PR REVIEW</span></div>
  </div>
 </div>
</template>
