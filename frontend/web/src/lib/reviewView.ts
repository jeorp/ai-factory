import type { Finding, Review } from '../types';
import { safeGitHubLink } from './github';

type PriorityLevel = 'high' | 'medium' | 'low' | 'unknown';

export const priorityLabels: Record<PriorityLevel, string> = { high: '高', medium: '中', low: '低', unknown: '未評価' };
const phaseLabels: Record<string, string> = { planning: '計画作成中', diff: '差分を確認中', cross: 'ファイル間の整合性を確認中', priority: '未確認項目の優先度を確認中', done: '確認完了' };
const stepStateLabels: Record<string, string> = { pending: '未処理', running: '確認中', done: '処理済み', partial: '未確認あり', excluded: '指定により対象外' };
const priorityOrder: Record<PriorityLevel, number> = { high: 0, medium: 1, low: 2, unknown: 3 };

export const reviewPhaseLabel = (phase: string) => phaseLabels[phase] || phase;
export const reviewStepStateLabel = (state: string) => stepStateLabels[state] || state;

type ReviewFile = Review['steps'][number]['files'][number];
export function reviewFileStatus(file: ReviewFile) {
 return file.excluded ? '指定により対象外（AI確認なし）' : file.done ? '処理済み' : file.offset ? '差分の途中まで処理済み' : '未処理';
}

/** Number of files the review has finished, including failed and excluded ones. */
export function processedFileCount(review: Review) {
 return review.steps.reduce((n, step) => n + step.files.filter(file => file.done).length, 0);
}

export function reviewScopeNote(review: Review) {
 if (review.scope === 'backend') return 'バックエンド（frontend/ を除外）';
 if (review.scope === 'frontend') return 'フロントエンド（backend/ を除外）';
 return '';
}

/**
 * Unverified items ordered by priority. Items without a priority yet are hidden
 * until the review is done, so the list does not reshuffle while it is scored.
 */
export function unverifiedItems(review?: Review) {
 if (!review) return [];
 return review.unverified.map((text, index) => ({ text, index, priority: review.priorities?.[index] }))
  .filter(item => !!item.priority || review.phase === 'done')
  .sort((a, b) => (priorityOrder[a.priority?.level || 'unknown'] ?? 3) - (priorityOrder[b.priority?.level || 'unknown'] ?? 3) || a.index - b.index);
}

/** GitHub blob link for a finding, at the base commit for removed lines and the head commit otherwise. */
export function findingLink(repository: string, review: Review, finding: Finding) {
 const file = review.steps.flatMap(step => step.files).find(file => file.path === finding.path);
 const sourcePath = finding.side === 'LEFT' ? file?.previousPath || finding.path : finding.path;
 const commit = finding.side === 'LEFT' ? review.base : review.head;
 return safeGitHubLink(`https://github.com/${repository}/blob/${commit}/${sourcePath.split('/').map(encodeURIComponent).join('/')}${finding.line ? '#L' + finding.line : ''}`);
}
