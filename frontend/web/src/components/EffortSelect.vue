<script setup lang="ts">
const props = defineProps<{ levels: string[]; value: string; disabled?: boolean; id: string }>();
const emit = defineEmits<{ change: [value: string] }>();
const labels: Record<string, string> = { minimal: '最小', low: '低', medium: '中', high: '高' };
// The select shows only the confirmed value: after emitting, the DOM is reset to
// the current prop. An accepted change updates the prop and re-renders; a
// rejected one (e.g. effort_not_allowed) leaves the previous value visible.
function onChange(event: Event) {
 const el = event.target as HTMLSelectElement;
 emit('change', el.value);
 el.value = props.value;
}
</script>

<template>
 <div v-if="levels.length" class="effort-select">
  <label :for="id">エフォート</label>
  <select :id="id" :value="value" :disabled="disabled" @change="onChange">
   <option v-for="level in levels" :key="level" :value="level">{{ labels[level] || level }}（{{ level }}）</option>
  </select>
 </div>
</template>
