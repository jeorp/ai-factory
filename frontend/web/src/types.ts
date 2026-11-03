export interface Source { label: string; url: string }
export interface Message { id: string; role: 'user' | 'assistant' | 'instruction'; text: string; at: string; applied: boolean }
export interface Session {
 tokenBudget?: number;
 id: string; title: string; target: { repository: string; commit: string; kind: string; number: number }; model: string; effort?: string;
 runKind?: string; activePullRequest?: number; review?: Review; implementation?: Implementation;
 state: string; messages: Message[]; summary: string; sources: Source[]; limitations: string[];
 metrics: { phases?: Record<string, { calls: number; inputTokens: number; outputTokens: number; durationMs: number }>; inputTokens: number; outputTokens: number; lastInputTokens?: number; lastOutputTokens?: number; hasLastUsage?: boolean; maxInputBytes: number; fetchedBytes: number; toolCalls: number; cacheHits: number; durationMs: number; heapPeakBytes: number };
 updatedAt: string; expiresAt: string;
}
export interface Repository { full_name: string; default_branch: string; private: boolean; html_url: string }
export interface Config { demo: boolean; aiProvider: 'demo' | 'openai' | 'gemini' | 'vertex' | 'anthropic'; models: string[]; modelEfforts?: Record<string, string[]>; modelEffortDefaults?: Record<string, string>; modelProviders?: Record<string, 'demo' | 'openai' | 'gemini' | 'vertex' | 'anthropic'>; installUrl: string; reviewPolicy?: { maxFiles: number; diffTokens: number; inputTokens: number; inputBytes: number; maxCalls: number; maxMinutes: number; runInputTokens: number; runOutputTokens: number } }
export const states: Record<string, string> = { accepted: '受付済み', running: '実行中', ci_wait: 'CIの完了待ち', user_wait: '続行待ち', auth_wait: 'AI認証待ち', done: '完了', stopping: '停止中', stopped: '停止済み', failed: '失敗' };
export const busy = (s?: Session | null) => !!s && ['running', 'accepted', 'stopping', 'ci_wait'].includes(s.state);

export interface Finding { severity: 'P0' | 'P1' | 'P2' | 'P3'; path: string; line: number; side: 'LEFT' | 'RIGHT'; title: string; problem: string; impact: string; evidence: string; suggestion: string }
export interface Review {
 requestMessageId?: string;
 scope?: 'all' | 'backend' | 'frontend'; title: string; body: string; base: string; head: string; totalFiles: number; phase: string;
 steps: { directory: string; state: string; summary: string; files: { path: string; previousPath: string; status: string; index: number; offset: number; done: boolean; excluded?: boolean }[] }[];
 findings: Finding[]; unverified: string[]; investigations?: Record<string, string>; pendingInvestigation?: number; priorities?: Record<string, { level: 'high' | 'medium' | 'low' | 'unknown'; reason: string }>; priorityCursor?: number; crossCursor: number; crossSummary: string;
}

export interface PlannedFile { path: string; action: 'create' | 'update' | 'delete'; approach: string; dependsOn?: string[] }
export interface AcceptanceCriterion { id: string; requirement: string; paths: string[]; checks: string[]; failureRefs?: string[] }
export interface Implementation {
 noChangeCandidates?: { path: string; reason: string }[];
 planAdjustments?: { file: PlannedFile; reason: string }[];
 request: string; baseBranch: string; baseCommit: string;
 status: 'planning' | 'plan_ready' | 'editing' | 'verifying' | 'verification_blocked' | 'amend_ready' | 'ready' | 'publishing' | 'checking_ci' | 'published';
 plan?: { summary: string; files: PlannedFile[]; outOfScope: string[] | null; risks: string[] | null; tests: string[] | null; acceptanceCriteria?: AcceptanceCriterion[] };
 steps?: { files: string[] }[]; nextStep: number;
 changes: { path: string; action: PlannedFile['action']; bytes: number }[]; unverified: string[]; summary: string; version: number;
 branch?: string; title?: string; body?: string; commit?: string; refUpdated?: boolean; pullNumber?: number; pullUrl?: string; expectedHead?: string;
 pendingFiles?: (PlannedFile & { reason: string })[]; declined?: string[];
 revising?: boolean; clarification?: string; repairRound?: number; changeRevision?: number;
 verification?: { issues: string[]; unverified: string[]; deferredChecks?: string[]; criteria?: { id: string; status: 'met' | 'unmet' | 'unverified'; evidence: string; paths: string[] }[] };
 ci?: { commit: string; state: 'pending' | 'passed' | 'failed' | 'unverified'; checks: { name: string; status: string; conclusion: string; jobId: number }[]; unverified: string[] };
}
