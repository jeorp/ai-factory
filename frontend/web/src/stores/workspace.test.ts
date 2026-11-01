import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { createPinia, setActivePinia } from 'pinia';
import { useWorkspace } from './workspace';
import type { Session } from '../types';

const session = (state: string): Session => ({ id: 'test-session', title: 'test', target: { repository: 'a/b', commit: 'c'.repeat(40), kind: 'repository', number: 0 }, model: 'gemini-test', state, messages: [], sources: [], limitations: [], summary: '', updatedAt: '', expiresAt: '', metrics: { inputTokens: 0, outputTokens: 0, maxInputBytes: 0, fetchedBytes: 0, toolCalls: 0, cacheHits: 0, durationMs: 0, heapPeakBytes: 0 } });
vi.mock('../lib/auth', () => ({ auth: { currentUser: { getIdToken: async () => 'test-token' } }, api: vi.fn(async () => session('failed')) }));
vi.mock('../lib/keyVault', () => ({ clearKey: vi.fn(), credentials: () => ({ apiKey: 'model-key', keyExpiresAt: 1 }) }));
vi.mock('../lib/fileCache', () => ({ clearFiles: vi.fn() }));

class Socket {
 static instance: Socket;
 static OPEN = 1;
 readyState = 1;
 onopen?: () => void;
 onmessage?: (event: { data: string }) => void;
 constructor() { Socket.instance = this; }
 send = vi.fn();
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

describe('automatic read-only CI waiting', () => {
 const waiting = (state = 'ci_wait'): Session => ({ ...session(state), runKind: 'implement', implementation: { request: 'fix', status: 'checking_ci', version: 1, baseBranch: 'main', baseCommit: 'c'.repeat(40), nextStep: 0, changes: [], unverified: [], summary: '', commit: 'd'.repeat(40), pullNumber: 7, ci: { commit: 'd'.repeat(40), state: 'pending', checks: [], unverified: [] } } });
 beforeEach(() => {
  vi.useFakeTimers(); setActivePinia(createPinia());
  vi.stubGlobal('WebSocket', Socket); vi.stubGlobal('location', { protocol: 'http:', host: '127.0.0.1:5175' });
 });
 afterEach(() => { vi.useRealTimers(); });
 it('continues a yielded CI wait without sending an AI key and without overlapping a running check', async () => {
  const work = useWorkspace(); await work.open(session('done'));
  Socket.instance.receive('session', waiting());
  await vi.advanceTimersByTimeAsync(10000);
  expect(Socket.instance.send).toHaveBeenCalledTimes(1);
  const sent = JSON.parse(Socket.instance.send.mock.calls[0]![0]);
  expect(sent.type).toBe('resume'); expect(sent.apiKey).toBeUndefined(); expect(sent.keyExpiresAt).toBeUndefined();
  Socket.instance.receive('session', waiting('running'));
  await vi.advanceTimersByTimeAsync(30000);
  expect(Socket.instance.send).toHaveBeenCalledTimes(1);
  Socket.instance.receive('session', waiting());
  await vi.advanceTimersByTimeAsync(10000);
  expect(Socket.instance.send).toHaveBeenCalledTimes(2);
  work.disconnect();
 });
 it.each(['stopped', 'failed', 'done'])('does not automatically resume a %s session', async state => {
  const work = useWorkspace(); await work.open(session('done'));
  Socket.instance.receive('session', waiting());
  Socket.instance.receive('session', waiting(state));
  await vi.advanceTimersByTimeAsync(10000);
  expect(Socket.instance.send).not.toHaveBeenCalled(); work.disconnect();
 });
 it('cancels a scheduled wait immediately on stop or disconnect', async () => {
  const work = useWorkspace(); await work.open(session('done'));
  Socket.instance.receive('session', waiting()); work.send('stop');
  await vi.advanceTimersByTimeAsync(10000);
  expect(Socket.instance.send).toHaveBeenCalledTimes(1);
  expect(JSON.parse(Socket.instance.send.mock.calls[0]![0]).type).toBe('stop');
  Socket.instance.receive('session', waiting()); work.disconnect();
  await vi.advanceTimersByTimeAsync(10000);
  expect(Socket.instance.send).toHaveBeenCalledTimes(1);
 });
 it('resumes CI after reconnecting but leaves other interrupted tasks alone', async () => {
  const work = useWorkspace(); await work.open(session('done'));
  Socket.instance.receive('session', waiting('auth_wait'));
  await vi.advanceTimersByTimeAsync(10000);
  expect(Socket.instance.send).toHaveBeenCalledTimes(1);
  const editing = waiting('auth_wait'); editing.implementation!.status = 'editing';
  Socket.instance.receive('session', editing);
  await vi.advanceTimersByTimeAsync(10000);
  expect(Socket.instance.send).toHaveBeenCalledTimes(1); work.disconnect();
 });
});
