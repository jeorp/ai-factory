import { fileKey, type FileRef, type SourceFile } from './sourceFiles';

export const fileTTL = 60 * 60 * 1000;
export const maxFileBytes = 100 * 1024;
export const maxCacheBytes = 10 * 1024 * 1024;
export interface SavedFile extends SourceFile { owner: string; session: string; savedAt: number }
const recordKey = (file: SavedFile) => JSON.stringify([file.owner, file.session, fileKey(file)]);
const bytes = (file: SavedFile) => new TextEncoder().encode(JSON.stringify(file)).length;
export function retainedFiles(files: SavedFile[], now = Date.now()): SavedFile[] {
 let total = 0;
 return files.filter(file => file.savedAt <= now && now - file.savedAt < fileTTL && new TextEncoder().encode(file.content).length <= maxFileBytes)
  .sort((a, b) => b.savedAt - a.savedAt).filter(file => { const size = bytes(file); if (total + size > maxCacheBytes) return false; total += size; return true; });
}
function openCache(): Promise<IDBDatabase> {
 return new Promise((resolve, reject) => {
  const request = indexedDB.open('ai-factory-source-files', 1);
  request.onupgradeneeded = () => request.result.createObjectStore('files');
  request.onsuccess = () => resolve(request.result);
  request.onerror = () => reject(request.error);
  request.onblocked = () => reject(new Error('file_cache_unavailable'));
 });
}
// Pruning and writes share one transaction, including across tabs.
async function changeCache(change: (files: SavedFile[]) => SavedFile[]): Promise<SavedFile[]> {
 const db = await openCache();
 try {
  return await new Promise((resolve, reject) => {
   const tx = db.transaction('files', 'readwrite'); const store = tx.objectStore('files'); const request = store.getAll(); let result: SavedFile[] = [];
   request.onsuccess = () => {
    result = retainedFiles(change(request.result as SavedFile[]));
    store.clear(); for (const file of result) store.put(file, recordKey(file));
   };
   tx.oncomplete = () => resolve(result);
   tx.onabort = tx.onerror = () => reject(tx.error || new Error('file_cache_unavailable'));
  });
 } finally { db.close(); }
}
export async function savedFiles(owner: string, session: string): Promise<SavedFile[]> {
 return (await changeCache(files => files)).filter(file => file.owner === owner && file.session === session);
}
export async function saveFile(owner: string, session: string, file: SourceFile): Promise<SavedFile[]> {
 if (new TextEncoder().encode(file.content).length > maxFileBytes) throw new Error('file_too_large');
 const saved = { ...file, owner, session, savedAt: Date.now() };
 const result = await changeCache(files => [...files.filter(old => recordKey(old) !== recordKey(saved)), saved]);
 if (!result.some(file => recordKey(file) === recordKey(saved))) throw new Error('file_cache_unavailable');
 return result.filter(file => file.owner === owner && file.session === session);
}
export async function clearFiles(owner: string, session?: string) {
 await changeCache(files => files.filter(file => file.owner !== owner || (session !== undefined && file.session !== session)));
}
export function findSaved(files: SavedFile[], ref: FileRef, now = Date.now()) {
 return files.find(file => fileKey(file) === fileKey(ref) && now >= file.savedAt && now - file.savedAt < fileTTL);
}
