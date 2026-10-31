import { afterEach, describe, expect, it, vi } from 'vitest';
import { clearKey, credentials, setKey } from './keyVault';

afterEach(() => { clearKey(); vi.useRealTimers(); });
describe('session key lifetime', () => {
 it('expires at exactly one hour even if browser timers were throttled', () => {
  vi.useFakeTimers(); const now = 1000; setKey('secret', now);
  expect(credentials(now + 3599999).apiKey).toBe('secret');
  expect(credentials(now + 3600000)).toEqual({ apiKey: '', keyExpiresAt: 0 });
 });
 it('does not extend expiry when credentials are read for another message', () => {
  vi.useFakeTimers(); setKey('secret', 1000); expect(credentials(2000).keyExpiresAt).toBe(3601000); clearKey(); expect(credentials(3000).apiKey).toBe('');
 });
});

describe('opt-in browser storage', () => {
 it('stores, migrates and deletes keys by provider', async () => {
  const data = new Map<string, string>(); const legacy = new Map<string, string>([['ai-factory.api-key.gemini', 'old-secret']]);
  vi.stubGlobal('localStorage', { getItem: (key: string) => legacy.get(key) ?? null, removeItem: (key: string) => legacy.delete(key) });
  vi.stubGlobal('indexedDB', { open: () => {
   const request: any = {}; const db = { close() {}, createObjectStore() {}, transaction: () => {
    const tx: any = { objectStore: () => ({
     get: (key: string) => operation(() => data.get(key)),
     put: (value: string, key: string) => operation(() => { data.set(key, value); return key; }),
     delete: (key: string) => operation(() => { data.delete(key); }),
    }) };
    function operation(fn: () => unknown) { const result: any = {}; queueMicrotask(() => { result.result = fn(); tx.oncomplete(); }); return result; }
    return tx;
   } };
   queueMicrotask(() => { request.result = db; request.onsuccess(); }); return request;
  } });
  const { saveKey, savedKey, deleteSavedKey } = await import('./keyVault');
  expect(await savedKey('gemini')).toBe('old-secret'); expect(legacy.size).toBe(0);
  await saveKey('openai', 'other-secret'); setKey('active-secret'); clearKey();
  expect(await savedKey('gemini')).toBe('old-secret'); expect(await savedKey('openai')).toBe('other-secret');
  await deleteSavedKey('gemini'); expect(await savedKey('gemini')).toBe('');
  vi.unstubAllGlobals();
 });
});
