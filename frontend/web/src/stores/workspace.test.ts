import { beforeEach, describe, expect, it, vi } from 'vitest';
import { createPinia, setActivePinia } from 'pinia';
import { useWorkspace } from './workspace';
import type { Session } from '../types';

const session = (state: string): Session => ({ id: 'test-session', title: 'test', target: { repository: 'a/b', commit: 'c'.repeat(40), kind: 'repository', number: 0 }, model: 'gemini-test', state, messages: [], sources: [], limitations: [], summary: '', updatedAt: '', expiresAt: '', metrics: { inputTokens: 0, outputTokens: 0, maxInputBytes: 0, fetchedBytes: 0, toolCalls: 0, cacheHits: 0, durationMs: 0, heapPeakBytes: 0 } });
vi.mock('../lib/auth', () => ({ auth: { currentUser: { getIdToken: async () => 'test-token' } }, api: vi.fn(async () => session('failed')) }));
vi.mock('../lib/keyVault', () => ({ clearKey: vi.fn(), credentials: () => ({}) }));
vi.mock('../lib/fileCache', () => ({ clearFiles: vi.fn() }));

class Socket {
 static instance: Socket;
 static OPEN = 1;
 readyState = 1;
 onopen?: () => void;
 onmessage?: (event: { data: string }) => void;
 constructor() { Socket.instance = this; }
 send() {}
 close() {}
 receive(type: string, data: unknown) { this.onmessage?.({ data: JSON.stringify({ type, data }) }); }
}

describe('run progress after failure and reconnection', () => {
 beforeEach(() => {
  setActivePinia(createPinia());
  vi.stubGlobal('WebSocket', Socket);
  vi.stubGlobal('location', { protocol: 'http:', host: '127.0.0.1:5175' });
 });
 it('clears stale running text and ignores late progress after a failed state', async () => {
  const work = useWorkspace();
  await work.open(session('failed'));
  Socket.instance.receive('session', session('running'));
  Socket.instance.receive('progress', '計画を作成中');
  expect(work.progress).toBe('計画を作成中');
  Socket.instance.receive('session', session('failed'));
  Socket.instance.receive('progress', '計画を作成中');
  expect(work.progress).toBe('');
  expect(work.current?.state).toBe('failed');
  work.disconnect();
 });
 it('starts a reopened conversation without the previous progress message', async () => {
  const work = useWorkspace();
  work.progress = '古い実行の進捗';
  await work.open(session('failed'));
  expect(work.progress).toBe('');
  work.disconnect();
 });
});
