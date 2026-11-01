<script setup lang="ts">
import { states, type Session } from '../types';
import { shortDate } from '../lib/sessionView';
import Icon from './Icon.vue';

defineProps<{ login: string; demo: boolean; sessions: Session[]; currentId?: string }>();
const deleteId = defineModel<string>('deleteId', { required: true });
defineEmits<{ create: []; open: [session: Session]; remove: [id: string]; signOut: [] }>();
</script>

<template>
 <aside class="sidebar">
  <a class="brand" href="/" aria-label="AI Factory ホーム"><span class="brand-symbol"><Icon name="grid" :size="19" /></span><span>AI Factory<span class="brand-dot">.</span></span></a>
  <div class="workspace-label">PERSONAL WORKSPACE <span class="tiny-tag">MVP</span></div>
  <button class="new-chat" :disabled="!login" aria-label="新しいセッション" @click="$emit('create')"><Icon name="plus" :size="17" />新しいセッション</button>
  <div class="nav-active"><Icon name="chat" :size="17" />GitHub チャット<span class="count">{{ sessions.length }}</span></div>
  <div class="section-caption">最近のセッション</div>
  <div class="session-list">
   <p v-if="!sessions.length" class="empty-sidebar">セッションを作成すると、<br />ここから会話に戻れます。</p>
   <div v-for="s in sessions" :key="s.id" class="session-item" :class="{ selected: currentId === s.id }">
    <button class="session-select" :aria-label="`${s.target.repository}の会話（${shortDate(s.updatedAt)}）`" @click="$emit('open', s)"><Icon name="chat" :size="15" /><span><strong>{{ s.target.repository }}</strong><small>{{ shortDate(s.updatedAt) }} · {{ states[s.state] || s.state }}</small></span></button>
    <button class="icon-button delete-button" :aria-label="`${s.title}を削除`" @click="deleteId = deleteId === s.id ? '' : s.id"><Icon name="trash" :size="14" /></button>
    <div v-if="deleteId === s.id" class="delete-confirm">会話と結果を削除します。<button @click="$emit('remove', s.id)">削除する</button></div>
   </div>
  </div>
  <div class="sidebar-bottom">
   <div class="readonly-note"><Icon name="shield" :size="16" /><span>GitHubへの書き込みは<br /><strong>確認後のPR作成のみ</strong>です</span></div>
   <div class="profile"><span class="avatar">{{ login ? login[0]?.toUpperCase() : 'G' }}</span><div><strong>{{ login || 'ゲスト' }}</strong><small>{{ demo ? 'ローカルデモ' : 'Personal account' }}</small></div><button v-if="login" class="icon-button" aria-label="ログアウト" @click="$emit('signOut')"><Icon name="logout" :size="17" /></button></div>
  </div>
 </aside>
</template>
