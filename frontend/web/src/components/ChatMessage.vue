<script setup lang="ts">
import { computed } from 'vue';
import type { Message } from '../types';
import { implementationCIMessageLink } from '../lib/implementationStatus';
import Icon from './Icon.vue';

const props = defineProps<{ message: Message; login: string; repository: string }>();
const label = computed(() => props.message.role === 'assistant' ? 'AI Factory' : props.message.role === 'instruction' ? '追加指示' : 'あなた');
const ciLink = computed(() => props.message.role === 'assistant' ? implementationCIMessageLink(props.repository, props.message.text) : '');
</script>

<template>
 <article class="message" :class="message.role">
  <div class="message-avatar"><Icon v-if="message.role === 'assistant'" name="grid" :size="16" /><Icon v-else-if="message.role === 'instruction'" name="branch" :size="16" /><span v-else>{{ login[0]?.toUpperCase() }}</span></div>
  <div class="message-content">
   <div class="message-label">{{ label }}<span v-if="message.role === 'instruction'" class="instruction-state">{{ message.applied ? '反映済み' : '受付済み・次の区切りで反映' }}</span></div>
   <div class="message-text"><a v-if="ciLink" :href="ciLink" target="_blank" rel="noopener noreferrer">CIを確認</a><template v-else>{{ message.text }}</template></div>
  </div>
 </article>
</template>
