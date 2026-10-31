// Active credentials remain in memory and expire after one hour.
let value = '';
let expiresAt = 0;
let timer: ReturnType<typeof setTimeout> | undefined;
export function clearKey() { value = ''; expiresAt = 0; clearTimeout(timer); }
export function setKey(key: string, now = Date.now()) { clearKey(); value = key; expiresAt = now + 60 * 60 * 1000; timer = setTimeout(clearKey, 60 * 60 * 1000); }
export function credentials(now = Date.now()) { if (now >= expiresAt) clearKey(); return { apiKey: value, keyExpiresAt: expiresAt }; }

// Saved keys are opt-in and scoped to the AI provider on this browser.
const legacyKey = (provider: string) => `ai-factory.api-key.${provider}`;
function openVault(): Promise<IDBDatabase> {
 return new Promise((resolve, reject) => {
  const request = indexedDB.open('ai-factory-key-vault', 1);
  request.onupgradeneeded = () => { request.result.createObjectStore('keys'); };
  request.onsuccess = () => resolve(request.result);
  request.onerror = () => reject(request.error);
  request.onblocked = () => reject(new Error('IndexedDB blocked'));
 });
}
async function transact<T>(mode: IDBTransactionMode, operation: (store: IDBObjectStore) => IDBRequest<T>): Promise<T> {
 const db = await openVault();
 try {
  return await new Promise<T>((resolve, reject) => {
   const tx = db.transaction('keys', mode); const request = operation(tx.objectStore('keys'));
   tx.oncomplete = () => resolve(request.result);
   tx.onabort = tx.onerror = () => reject(tx.error || request.error || new Error('IndexedDB transaction failed'));
  });
 } finally { db.close(); }
}
export async function savedKey(provider: string): Promise<string> {
 const stored = await transact('readonly', store => store.get(provider)) as string | undefined;
 const legacy = localStorage.getItem(legacyKey(provider));
 if (legacy) {
  if (!stored) await saveKey(provider, legacy);
  localStorage.removeItem(legacyKey(provider));
 }
 return stored || legacy || '';
}
export async function saveKey(provider: string, key: string) { await transact('readwrite', store => store.put(key, provider)); }
export async function deleteSavedKey(provider: string) {
 await transact('readwrite', store => store.delete(provider));
 localStorage.removeItem(legacyKey(provider));
}
