import { beforeEach, describe, expect, it, vi } from 'vitest';

const vault = vi.hoisted(() => ({ saved: '' as string | undefined, key: '' }));
vi.mock('../lib/keyVault', () => ({
 savedKey: vi.fn(async () => vault.saved),
 saveKey: vi.fn(async (_: string, key: string) => { vault.saved = key; }),
 deleteSavedKey: vi.fn(async () => { vault.saved = undefined; }),
 setKey: vi.fn((key: string) => { vault.key = key; }),
 clearKey: vi.fn(() => { vault.key = ''; }),
 credentials: vi.fn(() => ({ apiKey: vault.key, keyExpiresAt: 0 })),
}));
const { useApiKey } = await import('./useApiKey');

function setup(sessionId = () => 's1' as string | undefined) {
 const errors: string[] = [];
 const keys = useApiKey({ provider: () => 'openai', sessionId, onError: message => { if (message) errors.push(message); } });
 return { keys, errors };
}

describe('API key card state', () => {
 beforeEach(() => { vault.saved = undefined; vault.key = ''; });
 it('requires consent before connecting a key', async () => {
  const { keys, errors } = setup();
  keys.draft = 'sk-test';
  await keys.apply();
  expect(keys.valid).toBe(false);
  expect(errors).toHaveLength(1);
  keys.consent = true;
  await keys.apply();
  expect(keys.valid).toBe(true);
  expect(keys.draft).toBe('');
 });
 it('saves the key when asked and offers it next time', async () => {
  const { keys } = setup();
  Object.assign(keys, { draft: 'sk-test', consent: true, remember: true });
  await keys.apply();
  expect(vault.saved).toBe('sk-test');
  const next = setup().keys;
  await next.refreshSaved();
  expect(next.hasSaved).toBe(true);
  expect(next.choice).toBe('saved');
  await next.forgetSaved();
  expect(next.hasSaved).toBe(false);
  expect(next.valid).toBe(false);
 });
 it('drops a key chosen for a session the user has left', async () => {
  let id: string | undefined = 's1';
  vault.saved = 'sk-saved';
  const { keys } = setup(() => id);
  Object.assign(keys, { choice: 'saved', consent: true });
  const pending = keys.apply(); id = 's2'; await pending;
  expect(vault.key).toBe('');
  expect(keys.pending).toBe(false);
 });
});
