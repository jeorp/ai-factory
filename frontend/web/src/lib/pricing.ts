// Standard text-token rates in USD per million tokens, checked 2026-10-31.
// Cached inputs are not reported separately, so all inputs use the standard rate.
const rates: Record<string, Record<string, { input: number; output: number; url: string }>> = {
 openai: {
  'gpt-5-mini': { input: 0.25, output: 2, url: 'https://developers.openai.com/api/docs/models/gpt-5-mini' },
  'gpt-5-mini-2025-08-07': { input: 0.25, output: 2, url: 'https://developers.openai.com/api/docs/models/gpt-5-mini' },
 },
 gemini: { 'gemini-3.5-flash-lite': { input: 0.3, output: 2.5, url: 'https://ai.google.dev/gemini-api/docs/pricing' } },
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
