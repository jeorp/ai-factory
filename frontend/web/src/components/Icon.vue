<script setup lang="ts">
withDefaults(defineProps<{ name: string; size?: number }>(), { size: 20 });
const paths: Record<string, string> = {
 grid: 'M3 3h7v7H3z M14 3h7v7h-7z M3 14h7v7H3z M14 14h7v7h-7z',
 copy: 'M9 9h12v12H9z M15 9V3H3v12h6',
 plus: 'M12 5v14 M5 12h14', chat: 'M21 11.5a8.4 8.4 0 0 1-.9 3.8 8.5 8.5 0 0 1-7.6 4.7 8.4 8.4 0 0 1-3.8-.9L3 21l1.9-5.7a8.4 8.4 0 0 1-.9-3.8 8.5 8.5 0 0 1 4.7-7.6 8.4 8.4 0 0 1 3.8-.9h.5a8.5 8.5 0 0 1 8 8v.5z',
 code: 'm8 7-5 5 5 5 M16 7l5 5-5 5 M14 4l-4 16', arrow: 'M12 19V5 M5 12l7-7 7 7',
 link: 'M10 13a5 5 0 0 0 7 0l3-3a5 5 0 0 0-7-7l-2 2 M14 11a5 5 0 0 0-7 0l-3 3a5 5 0 0 0 7 7l2-2',
 key: 'M15.5 7.5a5 5 0 1 0-2 8L8 21H3v-5l5.5-5.5 M16 7h.01',
 external: 'M15 3h6v6 M10 14 21 3 M21 14v7H3V3h7',
 check: 'm5 12 4 4L19 6', stop: 'M6 6h12v12H6z', trash: 'M3 6h18 M9 6V3h6v3 M5 6l1 15h12l1-15 M10 10v7 M14 10v7',
 clock: 'M12 8v4l3 2 M22 12a10 10 0 1 1-20 0 10 10 0 0 1 20 0',
 shield: 'M12 3 3 7v5c0 5 9 9 9 9s9-4 9-9V7l-9-4z M8 12l3 3 5-6',
 branch: 'M6 3v12 M18 6v3c0 5-12 2-12 7 M9 18a3 3 0 1 1-6 0 3 3 0 0 1 6 0 M21 3a3 3 0 1 1-6 0 3 3 0 0 1 6 0',
 logout: 'M9 21H3V3h6 M16 17l5-5-5-5 M8 12h13',
 folder: 'M3 5h6l2 2h10v13H3V5z', chevron: 'm9 5 7 7-7 7', refresh: 'M20 7v5h-5 M4 17v-5h5 M5.6 7a7 7 0 0 1 11.6-2L20 8 M4 16l2.8 3A7 7 0 0 0 18.4 17',
};
</script>
<template><svg :width="size" :height="size" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.65" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path :d="paths[name] || paths.chat" /></svg></template>
