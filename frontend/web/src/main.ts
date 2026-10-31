import { createApp } from 'vue';
import { createPinia } from 'pinia';
import { createRouter, createWebHistory } from 'vue-router';
import App from './App.vue';
import './style.css';
import './responsive.css';

const router = createRouter({ history: createWebHistory(), routes: [{ path: '/:pathMatch(.*)*', component: { template: '<span />' } }] });
createApp(App).use(createPinia()).use(router).mount('#app');
