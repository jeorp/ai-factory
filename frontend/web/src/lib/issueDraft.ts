import type { Finding } from '../types';

export function issueDraft(finding: Finding, repository: string, pullNumber: number, sourceUrl: string) {
 const repoPath = repository.split('/').map(encodeURIComponent).join('/');
 const base = `https://github.com/${repoPath}`;
 const title = `[${finding.severity}] ${finding.title}`;
 const location = `${finding.path}${finding.line ? `:${finding.line}` : ''}（${finding.side === 'LEFT' ? '変更前' : '変更後'}）`;
 const body = [
  `関連 PR: ${base}/pull/${pullNumber}`,
  `対象: [${location}](${sourceUrl})`,
  `重要度: ${finding.severity}`,
  `## 問題\n\n${finding.problem}`,
  `## 影響\n\n${finding.impact}`,
  `## 根拠\n\n${finding.evidence}`,
  ...(finding.suggestion ? [`## 修正案\n\n${finding.suggestion}`] : []),
 ].join('\n\n');
 const issueUrl = `${base}/issues/new`;
 const fullUrl = `${issueUrl}?${new URLSearchParams({ title, body })}`;
 // Leave room for intermediaries with modest URL limits; preserve the entire draft on fallback.
 return { title, body, fullUrl: fullUrl.length <= 7000 ? fullUrl : '', fallbackUrl: `${issueUrl}?${new URLSearchParams({ title })}` };
}
