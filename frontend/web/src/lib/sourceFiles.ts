import type { Session } from '../types';

export interface FileRef { repository: string; commit: string; path: string }
export interface SourceFile extends FileRef { content: string; url: string; cached: boolean }
const sha = /^[a-f0-9]{40}$/i;
export function fileKey(file: FileRef) { return JSON.stringify([file.repository, file.commit, file.path]); }
export function isFilePath(path: string) {
 return !!path && path.length <= 512 && !path.startsWith('/') && !path.includes('..') && !/[\\\x00\r\n:?#]/.test(path) && !path.endsWith('/') && !/\.(?:png|jpe?g|gif|webp|pdf|zip|mp4|woff2?|exe|lock|map|min\.js)$/i.test(path);
}
// Source links carry their exact commit; review paths use head (or base for deletions).
export function listedFiles(session: Session): FileRef[] {
 const files = new Map<string, FileRef>();
 const add = (path: string, commit: string) => {
  const file = { repository: session.target.repository, commit, path };
  if (isFilePath(path) && sha.test(commit) && files.size < 200) files.set(fileKey(file), file);
 };
 for (const source of session.sources) {
  try {
   const url = new URL(source.url); const prefix = `/${session.target.repository}/blob/`;
   if (url.protocol !== 'https:' || url.host !== 'github.com' || url.username || !url.pathname.startsWith(prefix)) continue;
   const [commit = '', ...parts] = url.pathname.slice(prefix.length).split('/');
   add(decodeURIComponent(parts.join('/')), commit);
  } catch { /* Ignore non-file links. */ }
 }
 if (session.review) for (const step of session.review.steps) for (const file of step.files) {
  add(file.path, file.status === 'removed' ? session.review.base : session.review.head);
 }
 if (session.review) for (const finding of session.review.findings || []) {
  const file = session.review.steps.flatMap(step => step.files).find(file => file.path === finding.path);
  add(finding.side === 'LEFT' ? file?.previousPath || finding.path : finding.path, finding.side === 'LEFT' ? session.review.base : session.review.head);
 }
 // Include paths enumerated in ordinary chat, even when only the directory was read.
 for (const message of session.messages) if (message.role === 'assistant') {
  const paths = message.text.replace(/https?:\/\/[^\s`<>]+/g, '').matchAll(/(?:`([^`\n]+)`)|(?:\b((?:[\w@.-]+\/)+[\w@.-]+\.[a-zA-Z0-9]+))/g);
  for (const match of paths) {
   const path = (match[1] || match[2] || '').replace(/:\d+(?:-\d+)?$/, '');
   if (/\.[a-zA-Z][a-zA-Z0-9]*$/.test(path) && ![...files.values()].some(file => file.path === path)) add(path, session.review?.head || session.target.commit);
  }
 }
 return [...files.values()];
}
