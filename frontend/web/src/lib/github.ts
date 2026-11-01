/** Returns the URL only when it points at https://github.com, otherwise a harmless '#'. */
export function safeGitHubLink(url: string) {
 try { const u = new URL(url); return u.protocol === 'https:' && u.hostname === 'github.com' ? u.href : '#'; }
 catch { return '#'; }
}
