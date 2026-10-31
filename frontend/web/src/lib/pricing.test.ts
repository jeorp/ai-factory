import { describe, expect, it } from 'vitest';
import { estimateCost, formatCost, pricing } from './pricing';
describe('token cost estimates', () => {
 it('uses the standard input and output rates for each supported provider', () => {
  expect(estimateCost('openai', 'gpt-5-mini', 1_000_000, 1_000_000)).toBe(2.25);
  expect(estimateCost('gemini', 'gemini-3.5-flash-lite', 1_000_000, 1_000_000)).toBe(2.8);
 });
 it('does not infer rates for unknown models or demo mode', () => {
  expect(pricing('openai', 'gpt-5-mini-extra')).toBeUndefined();
  expect(estimateCost('demo', 'gpt-5-mini', 100, 100)).toBeUndefined();
 });
 it('does not round small nonzero costs down to zero', () => {
  expect(formatCost(0)).toBe('$0.0000');
  expect(formatCost(0.000001)).toBe('< $0.0001');
  expect(formatCost(0.01234)).toBe('$0.0123');
 });
});
