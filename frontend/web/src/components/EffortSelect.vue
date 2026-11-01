<script setup lang="ts">
defineProps<{ levels: string[]; value: string; disabled?: boolean; id: string }>();
defineEmits<{ change: [value: string] }>();
const labels: Record<string, string> = { minimal: '最小', low: '低', medium: '中', high: '高' };
</script>

<template>
 <div v-if="levels.length" class="effort-select">
  <label :for="id">エフォート</label>
  <select :id="id" :value="value" :disabled="disabled" @change="$emit('change', ($event.target as HTMLSelectElement).value)">
   <option v-for="level in levels" :key="level" :value="level">{{ labels[level] || level }}（{{ level }}）</option>
  </select>
 </div>
</template>
