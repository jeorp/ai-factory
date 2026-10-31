import { describe, expect, it } from 'vitest';
import { highlightCode } from './codeHighlight';

describe('source code rendering', () => {
 it('escapes executable markup in both highlighted HTML and unknown files', () => {
  const content = '<script>alert("test")</script><img src=x onerror="alert(1)"> & text';
  for (const path of ['index.html', 'example.unknown']) {
   const result = highlightCode(path, content);
   expect(result.html).not.toContain('<script>');
   expect(result.html).not.toContain('<img ');
   expect(result.html).toContain('&lt;');
   expect(result.html).toContain('&amp;');
  }
 });
 it('highlights the complete file so multiline comments retain their scope', () => {
  const result = highlightCode('handler.go', 'package main\n/* first\nsecond */\nfunc main() {}\n');
  expect(result.language).toBe('Go');
  expect(result.html).toContain('<span class="hljs-comment">/* first\nsecond */</span>');
  expect(result.html).toContain('hljs-keyword');
  expect(result.numbers).toBe('1\n2\n3\n4');
 });
 it('keeps blank lines and aligns CRLF line numbers without a phantom trailing line', () => {
  const result = highlightCode('src/client.ts', 'const name = "日本語";\r\n\r\nexport { name };\r\n');
  expect(result.language).toBe('TypeScript');
  expect(result.lines).toBe(3);
  expect(result.numbers).toBe('1\n2\n3');
  expect(result.html).not.toContain('\r');
  expect(result.bytes).toBe(new TextEncoder().encode('const name = "日本語";\r\n\r\nexport { name };\r\n').length);
  expect(highlightCode('empty.ts', '').lines).toBe(0);
  expect(highlightCode('one.ts', '\n').lines).toBe(1);
 });
});
