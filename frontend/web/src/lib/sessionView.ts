import type { Config, Session } from '../types';

export type ComposerMode = 'message' | 'implement' | 'instruction';

export type ReviewPolicy = NonNullable<Config['reviewPolicy']>;
export const defaultReviewPolicy: ReviewPolicy = { maxFiles: 10, diffTokens: 20000, inputTokens: 30000, inputBytes: 262144, maxCalls: 100, maxMinutes: 15, runInputTokens: 500000, runOutputTokens: 64000 };
const chatInputLimitBytes = 24576;

export function inputLimitBytes(session: Session | null | undefined, policy: ReviewPolicy) {
 return session?.runKind === 'review' ? policy.inputBytes : chatInputLimitBytes;
}

const resumableStates = ['auth_wait', 'failed', 'stopped', 'user_wait'];

/** Whether the session has saved progress the user can continue from. */
export function canResume(session: Session) {
 if (resumableStates.includes(session.state) && (session.messages.length || session.runKind === 'review' || session.runKind === 'implement')) return true;
 return session.state === 'done' && session.target?.kind === 'pull' && !!session.review?.phase && session.review.phase !== 'done';
}

export function shortDate(value: string) {
 return new Intl.DateTimeFormat('ja-JP', { month: 'short', day: 'numeric' }).format(new Date(value));
}
