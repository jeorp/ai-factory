// Deliberately module memory only: never localStorage, sessionStorage, Pinia persistence or URL.
let value = '';
let expiresAt = 0;
let timer: ReturnType<typeof setTimeout> | undefined;
export function clearKey() { value = ''; expiresAt = 0; clearTimeout(timer); }
export function setKey(key: string, now = Date.now()) { clearKey(); value = key; expiresAt = now + 60 * 60 * 1000; timer = setTimeout(clearKey, 60 * 60 * 1000); }
export function credentials(now = Date.now()) { if (now >= expiresAt) clearKey(); return { apiKey: value, keyExpiresAt: expiresAt }; }
