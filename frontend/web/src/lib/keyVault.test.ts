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
