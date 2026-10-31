import { initializeApp } from 'firebase/app';
import { browserSessionPersistence, connectAuthEmulator, getAuth, setPersistence, signInWithCustomToken, signOut } from 'firebase/auth';

const local = import.meta.env.DEV || import.meta.env.MODE === 'emulator';
const project = import.meta.env.VITE_FIREBASE_PROJECT_ID || (local ? 'demo-ai-factory' : '');
export const auth = getAuth(initializeApp({ projectId: project, apiKey: import.meta.env.VITE_FIREBASE_API_KEY || 'local-emulator-key', authDomain: import.meta.env.VITE_FIREBASE_AUTH_DOMAIN || `${project}.firebaseapp.com` }));
if (local) connectAuthEmulator(auth, import.meta.env.VITE_AUTH_EMULATOR_URL || 'http://127.0.0.1:9197', { disableWarnings: true });
export async function loginComplete() {
 const response = await fetch('/auth/complete', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: '{}' });
 const value = await response.json(); if (!response.ok) throw new Error(value.error);
 await setPersistence(auth, browserSessionPersistence);
 await signInWithCustomToken(auth, value.customToken);
}
export async function logout() { await fetch('/auth/logout', { method: 'POST' }); await signOut(auth); }
export async function api<T>(path: string, options: RequestInit = {}): Promise<T> {
 const token = await auth.currentUser?.getIdToken();
 const response = await fetch(`/api/v1${path}`, { ...options, headers: { 'Content-Type': 'application/json', Authorization: `Bearer ${token || ''}`, ...options.headers } });
 const value = await response.json(); if (!response.ok) throw new Error(value.error || 'request_failed'); return value;
}
