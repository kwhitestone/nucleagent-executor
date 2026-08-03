/**
 * 子应用 ↔ 主壳 的 postMessage 通道桥接（iframe 方案，auth/executor 简化版）。
 *
 * 仅同步登录态：iframe 跨域 localStorage 不共享，壳登录后把 token 推过来，
 * 写入本子应用域的 localStorage，否则接口 401。
 *
 * 仅在被 iframe 嵌入时（window.parent !== window）生效。
 */
export function isInShell(): boolean {
  return typeof window !== "undefined" && window.parent !== window;
}

export function useShellBridge(): void {
  if (!isInShell()) return;

  function onMessage(e: MessageEvent): void {
    const d = e.data as { source?: string; type?: string; token?: string | null };
    if (d?.source !== "shell" || d.type !== "auth") return;
    const KEY = "nucleagent_access_token";
    const RKEY = "nucleagent_refresh_token";
    if (d.token) {
      localStorage.setItem(KEY, d.token);
    } else {
      localStorage.removeItem(KEY);
      localStorage.removeItem(RKEY);
    }
  }

  window.addEventListener("message", onMessage);
}
