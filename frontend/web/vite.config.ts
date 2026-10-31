import { defineConfig, loadEnv } from 'vite';
import vue from '@vitejs/plugin-vue';

export default defineConfig(({ command, mode }) => {
 const env = { ...loadEnv(mode, process.cwd(), ''), ...process.env };
 if (command === 'build' && mode !== 'emulator' && (!env.VITE_FIREBASE_API_KEY || !env.VITE_FIREBASE_PROJECT_ID || env.VITE_FIREBASE_PROJECT_ID.startsWith('demo-') || env.VITE_AUTH_EMULATOR_URL)) throw new Error('本番用Firebase設定が必要です。ローカル確認には build:local を使ってください。');
 const target = env.API_PROXY_TARGET || 'http://127.0.0.1:8181';
 const proxy = { '/api': { target, ws: true }, '/auth': { target }, '/health': { target } };
 return { plugins: [vue()], server: { proxy }, preview: { proxy } };
});
