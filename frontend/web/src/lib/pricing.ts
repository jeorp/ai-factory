// Standard text-token rates in USD per million tokens, checked 2026-10-31.
// Cached inputs are not reported separately, so all inputs use the standard rate.
const gp = 'https://ai.google.dev/gemini-api/docs/pricing';
const rates: Record<string, Record<string, { input: number; output: number; url: string }>> = {
 openai: {
  'gpt-5-mini': { input: 0.25, output: 2, url: 'https://developers.openai.com/api/docs/models/gpt-5-mini' },
  'gpt-5-mini-2025-08-07': { input: 0.25, output: 2, url: 'https://developers.openai.com/api/docs/models/gpt-5-mini' },
 },
 // Claude API first-party rates for prompts up to 100K tokens (Haiku 5.5 doubles beyond that).
 anthropic: {
  'claude-haiku-5-5': { input: 0.1, output: 0.5, url: 'https://platform.claude.com/docs/en/about-claude/pricing' },
  'claude-sonnet-5-5': { input: 2, output: 10, url: 'https://platform.claude.com/docs/en/about-claude/pricing' },
 },
 // Gemini rates use the <=200k-token prompt tier. Some Flash and Gemini 4 rates are
 // scheduled to rise (3.6/3.8 Flash on 2027-01-01, Gemini 4 Argon on 2026-12-01); update then.
 gemini: {
  'gemini-3.5-flash-lite': { input: 0.3, output: 2.5, url: gp },
  'gemini-3.1-flash-lite': { input: 0.25, output: 1.5, url: gp },
  'gemini-3.6-flash': { input: 0.75, output: 3.75, url: gp },
  'gemini-3.8-flash': { input: 0.75, output: 3.75, url: gp },
  'gemini-3-flash-preview': { input: 0.5, output: 3, url: gp },
  'gemini-3.1-pro-preview': { input: 2, output: 12, url: gp },
  'gemini-4-argon': { input: 2, output: 10, url: gp },
  'gemini-2.5-pro': { input: 1.25, output: 10, url: gp },
  'gemini-2.5-flash': { input: 0.3, output: 2.5, url: gp },
 },
};
export function pricing(provider: string, model: string) { return rates[provider]?.[model]; }
export function estimateCost(provider: string, model: string, input: number, output: number): number | undefined {
 const rate = pricing(provider, model);
 return rate ? (input * rate.input + output * rate.output) / 1_000_000 : undefined;
}
export function formatCost(amount: number): string {
 if (amount > 0 && amount < 0.0001) return '< $0.0001';
 return `$${amount.toFixed(4)}`;
}
