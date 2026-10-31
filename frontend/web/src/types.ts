export interface Source { label: string; url: string }
export interface Message { id: string; role: 'user' | 'assistant' | 'instruction'; text: string; at: string; applied: boolean }
export interface Session {
 id: string; title: string; target: { repository: string; commit: string; kind: string; number: number }; model: string;
 state: string; messages: Message[]; summary: string; sources: Source[]; limitations: string[];
 metrics: { inputTokens: number; outputTokens: number; maxInputBytes: number; fetchedBytes: number; toolCalls: number; cacheHits: number; durationMs: number; heapPeakBytes: number };
 updatedAt: string; expiresAt: string;
}
export interface Repository { full_name: string; default_branch: string; private: boolean; html_url: string }
export interface Config { demo: boolean; models: string[]; installUrl: string }
export const states: Record<string, string> = { accepted: '受付済み', running: '実行中', user_wait: 'ユーザー待ち', auth_wait: 'AI認証待ち', done: '完了', stopping: '停止中', stopped: '停止済み', failed: '失敗' };
export const busy = (s?: Session | null) => !!s && ['running', 'accepted', 'stopping'].includes(s.state);
