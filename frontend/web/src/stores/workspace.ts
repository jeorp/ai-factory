import { defineStore } from 'pinia';
import { ref } from 'vue';
import { auth, api } from '../lib/auth';
import { clearKey, credentials } from '../lib/keyVault';
import { clearFiles } from '../lib/fileCache';
import type { Session } from '../types';

export const useWorkspace = defineStore('workspace', () => {
 const sessions = ref<Session[]>([]); const current = ref<Session | null>(null); const connected = ref(false); const progress = ref(''); const error = ref(''); const reconnecting = ref(false); const traceId = ref('');
 let socket: WebSocket | undefined; let generation = 0; let retry: ReturnType<typeof setTimeout> | undefined; let attempts = 0;
 async function refresh() { sessions.value = await api<Session[]>('/sessions'); }
 function disconnect(clear = false) { generation++; clearTimeout(retry); socket?.close(); socket = undefined; connected.value = false; reconnecting.value = false; if (clear) { clearKey(); current.value = null; } }
 function normalize(session: Session) {
  session.messages ||= []; session.sources ||= []; session.limitations ||= [];
  if (session.implementation) { session.implementation.changes ||= []; session.implementation.unverified ||= []; }
  if (session.review) { session.review.steps ||= []; session.review.findings ||= []; session.review.unverified ||= []; for (const step of session.review.steps) step.files ||= []; }
  return session;
 }
 function update(session: Session) { current.value = normalize(session); const i = sessions.value.findIndex(s => s.id === session.id); if (i >= 0) sessions.value[i] = session; else sessions.value.unshift(session); }
 async function open(session: Session) {
  if (current.value?.id !== session.id) { clearKey(); traceId.value = ''; } disconnect(); attempts = 0; error.value = '';
  const epoch = generation;
  const full = await api<Session>(`/sessions/${session.id}`); if (epoch !== generation) return;
  current.value = normalize(full); await connect(epoch);
 }
 async function connect(epoch: number) {
  try {
   const token = await auth.currentUser?.getIdToken(); if (epoch !== generation || !token || !current.value) return;
   const ws = new WebSocket(`${location.protocol === 'https:' ? 'wss:' : 'ws:'}//${location.host}/api/v1/sessions/${current.value.id}/ws`); socket = ws;
   ws.onopen = () => ws.send(JSON.stringify({ type: 'auth', token }));
   ws.onmessage = event => {
    if (epoch !== generation) return;
    const value = JSON.parse(event.data);
    if (value.type === 'session') { update(value.data); connected.value = true; reconnecting.value = false; attempts = 0; }
    if (value.type === 'progress') progress.value = value.data;
    if (value.type === 'trace') traceId.value = value.data.traceId;
    if (value.type === 'ended') disconnect(true);
    if (value.type === 'error') { error.value = value.data.error; if (value.data.error === 'login_required') { disconnect(true); } }
   };
   ws.onclose = () => {
    if (epoch !== generation) return; connected.value = false; reconnecting.value = true;
    progress.value = '接続が切れました。処理を停止し、保存済みの会話へ再接続しています。';
    const delay = Math.min(30000, 1000 * 2 ** attempts++); retry = setTimeout(() => void connect(epoch), delay);
   };
   ws.onerror = () => { error.value = 'connection_failed'; };
  } catch { if (epoch === generation) error.value = 'login_required'; }
 }
 function send(type: string, text = '', id = crypto.randomUUID(), unverifiedIndex?: number, extra: Record<string, string | number> = {}) {
  if (!connected.value || socket?.readyState !== WebSocket.OPEN) throw new Error('connection_required');
  // Publishing writes saved changes and never needs the AI key.
  socket.send(JSON.stringify({ type, text, id, unverifiedIndex, ...extra, ...(['message', 'resume', 'review', 'investigate', 'implement', 'implement_approve'].includes(type) ? credentials() : {}) }));
  error.value = '';
 }
 async function remove(id: string) {
  if (current.value?.id === id) disconnect(true);
  for (let attempt = 0; ; attempt++) {
   try { await api(`/sessions/${id}`, { method: 'DELETE' }); break; }
   catch (e) { if (!(e instanceof Error) || e.message !== 'session_busy' || attempt >= 5) throw e; await new Promise(resolve => setTimeout(resolve, 500)); }
  }
  sessions.value = sessions.value.filter(s => s.id !== id);
  if (auth.currentUser) await clearFiles(auth.currentUser.uid, id).catch(() => {});
 }
 return { sessions, current, connected, progress, error, reconnecting, traceId, refresh, open, disconnect, send, remove };
});
