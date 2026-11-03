import type { Documentation } from '../types';
import { isFilePath } from './sourceFiles';

export function documentationFileURL(doc: Documentation, path: string) {
 if (!/^[\w.-]+\/[\w.-]+$/.test(doc.repository) || doc.repository.includes('..') || !/^[a-f0-9]{40}$/i.test(doc.commit) || !isFilePath(path)) return '#';
 return `https://github.com/${doc.repository}/blob/${doc.commit}/${path.split('/').map(encodeURIComponent).join('/')}`;
}
export function documentationLimitations(doc: Documentation) { return [...new Set([...(doc.result?.unverified || []), ...(doc.limitations || [])])]; }
const oneLine = (text: string) => text.replace(/[\r\n]/g, ' ');
export function documentationMarkdown(doc: Documentation) {
 const result = doc.result;
 if (!result) return '';
 const lines = [`# ${oneLine(result.title)}`, '', result.summary, '', `対象: ${doc.repository}`, `コミット: ${doc.commit}`, `資料の範囲: ${doc.scope === 'feature' ? '機能・画面' : 'リポジトリ全体'}`, `依頼: ${oneLine(doc.request)}`, ''];
 for (const section of result.sections) {
  lines.push(`## ${oneLine(section.heading)}`, '', section.body, '');
  for (const path of section.paths) { const url = documentationFileURL(doc, path); if (url !== '#') lines.push(`- [${path.replace(/[\[\]]/g, '\\$&')}](${url})`); }
  if (section.paths.length) lines.push('');
 }
 const limits = documentationLimitations(doc);
 if (limits.length) lines.push('## 未確認事項・調査の範囲', '', ...limits.map(limit => `- ${oneLine(limit)}`), '');
 return lines.join('\n');
}
