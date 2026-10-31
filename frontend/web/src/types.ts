export interface Source { label: string; url: string }
export interface Message { id: string; role: 'user' | 'assistant' | 'instruction'; text: string; at: string; applied: boolean }
export interface Session {
 id: string; title: string; target: { repository: string; commit: string; kind: string; number: number }; model: string;
 runKind?: string; activePullRequest?: number; review?: Review;
 state: string; messages: Message[]; summary: string; sources: Source[]; limitations: string[];
 metrics: { inputTokens: number; outputTokens: number; lastInputTokens?: number; lastOutputTokens?: number; hasLastUsage?: boolean; maxInputBytes: number; fetchedBytes: number; toolCalls: number; cacheHits: number; durationMs: number; heapPeakBytes: number };
 updatedAt: string; expiresAt: string;
}
export interface Repository { full_name: string; default_branch: string; private: boolean; html_url: string }
export interface Config { demo: boolean; aiProvider: 'demo' | 'openai' | 'gemini'; models: string[]; installUrl: string; reviewPolicy?: { maxFiles: number; diffTokens: number; inputTokens: number; inputBytes: number; maxCalls: number; maxMinutes: number; runInputTokens: number; runOutputTokens: number } }
export const states: Record<string, string> = { accepted: '受付済み', running: '実行中', user_wait: '続行待ち', auth_wait: 'AI認証待ち', done: '完了', stopping: '停止中', stopped: '停止済み', failed: '失敗' };
export const busy = (s?: Session | null) => !!s && ['running', 'accepted', 'stopping'].includes(s.state);

export interface Finding { severity: 'P0' | 'P1' | 'P2' | 'P3'; path: string; line: number; side: 'LEFT' | 'RIGHT'; title: string; problem: string; impact: string; evidence: string; suggestion: string }
export interface Review {
 title: string; body: string; base: string; head: string; totalFiles: number; phase: string;
 steps: { directory: string; state: string; summary: string; files: { path: string; previousPath: string; status: string; index: number; offset: number; done: boolean }[] }[];
 findings: Finding[]; unverified: string[]; investigations?: Record<string, string>; pendingInvestigation?: number; priorities?: Record<string, { level: 'high' | 'medium' | 'low' | 'unknown'; reason: string }>; priorityCursor?: number; crossCursor: number; crossSummary: string;
}
