import hljs from 'highlight.js/lib/core';
import bash from 'highlight.js/lib/languages/bash';
import css from 'highlight.js/lib/languages/css';
import diff from 'highlight.js/lib/languages/diff';
import dockerfile from 'highlight.js/lib/languages/dockerfile';
import go from 'highlight.js/lib/languages/go';
import ini from 'highlight.js/lib/languages/ini';
import java from 'highlight.js/lib/languages/java';
import javascript from 'highlight.js/lib/languages/javascript';
import json from 'highlight.js/lib/languages/json';
import markdown from 'highlight.js/lib/languages/markdown';
import python from 'highlight.js/lib/languages/python';
import ruby from 'highlight.js/lib/languages/ruby';
import rust from 'highlight.js/lib/languages/rust';
import sql from 'highlight.js/lib/languages/sql';
import typescript from 'highlight.js/lib/languages/typescript';
import xml from 'highlight.js/lib/languages/xml';
import yaml from 'highlight.js/lib/languages/yaml';

for (const [name, grammar] of Object.entries({ bash, css, diff, dockerfile, go, ini, java, javascript, json, markdown, python, ruby, rust, sql, typescript, xml, yaml })) hljs.registerLanguage(name, grammar);

const languages: Record<string, [string, string]> = {
 go: ['go', 'Go'], ts: ['typescript', 'TypeScript'], tsx: ['typescript', 'TSX'], js: ['javascript', 'JavaScript'], jsx: ['javascript', 'JSX'], mjs: ['javascript', 'JavaScript'], cjs: ['javascript', 'JavaScript'],
 vue: ['xml', 'Vue'], html: ['xml', 'HTML'], htm: ['xml', 'HTML'], xml: ['xml', 'XML'], svg: ['xml', 'SVG'], css: ['css', 'CSS'],
 json: ['json', 'JSON'], jsonc: ['json', 'JSON'], yaml: ['yaml', 'YAML'], yml: ['yaml', 'YAML'], toml: ['ini', 'TOML'], ini: ['ini', 'INI'],
 py: ['python', 'Python'], java: ['java', 'Java'], rb: ['ruby', 'Ruby'], rs: ['rust', 'Rust'], sql: ['sql', 'SQL'],
 sh: ['bash', 'Shell'], bash: ['bash', 'Bash'], zsh: ['bash', 'Shell'], md: ['markdown', 'Markdown'], mdx: ['markdown', 'Markdown'], diff: ['diff', 'Diff'], patch: ['diff', 'Diff'],
};
function escapeHTML(content: string) {
 return content.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;').replace(/"/g, '&quot;').replace(/'/g, '&#x27;');
}

export function highlightCode(path: string, content: string) {
 const name = path.split('/').at(-1)?.toLowerCase() || '';
 const language = name === 'dockerfile' || name.startsWith('dockerfile.') ? ['dockerfile', 'Dockerfile'] : languages[name.split('.').at(-1) || ''];
 const text = content.replace(/\r\n?/g, '\n');
 const lines = text ? text.replace(/\n$/, '').split('\n').length : 0;
 let html = escapeHTML(text);
 if (language) {
  try { html = hljs.highlight(text, { language: language[0]!, ignoreIllegals: true }).value; }
  catch { /* Escaped plain text remains readable when a grammar cannot parse the file. */ }
 }
 return { html, language: language?.[1] || 'Text', lines, numbers: Array.from({ length: lines }, (_, i) => i + 1).join('\n'), bytes: new TextEncoder().encode(content).length };
}
