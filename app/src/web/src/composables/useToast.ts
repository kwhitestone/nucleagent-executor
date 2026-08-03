/**
 * 极简 toast，替代 Element Plus 的 ElMessage。
 *
 * 实现策略：往 body 挂一个临时容器，N 毫秒后移除。比引入完整 EP 轻得多，
 * 且样式完全用 Aurora token，与设计稿一致。
 *
 * 用法：import { toast } from "@/composables/useToast"; toast.error("失败");
 */
type ToastType = "success" | "error" | "info" | "warning";

const TYPE_COLOR: Record<ToastType, string> = {
  success: "var(--emerald-500)",
  error: "var(--rose-500)",
  warning: "var(--amber-500)",
  info: "var(--indigo-500)",
};

let container: HTMLDivElement | null = null;

function getContainer(): HTMLDivElement {
  if (!container) {
    container = document.createElement("div");
    container.style.cssText =
      "position:fixed;top:20px;left:50%;transform:translateX(-50%);z-index:9999;display:flex;flex-direction:column;gap:8px;pointer-events:none;";
    document.body.appendChild(container);
  }
  return container;
}

export const toast = {
  show(message: string, type: ToastType = "info", duration = 2500): void {
    const el = document.createElement("div");
    el.textContent = message;
    el.style.cssText = `
      pointer-events:auto;
      background:var(--bg-card);
      color:var(--text-primary);
      border:1px solid var(--border);
      border-left:3px solid ${TYPE_COLOR[type]};
      padding:10px 16px;
      border-radius:var(--r-md);
      box-shadow:var(--shadow-lg);
      font-size:13.5px;font-family:var(--font-body);
      max-width:380px;
      animation:fade-in-up 0.3s var(--ease-out) both;
    `;
    getContainer().appendChild(el);
    setTimeout(() => {
      el.style.transition = "opacity 0.3s, transform 0.3s";
      el.style.opacity = "0";
      el.style.transform = "translateY(-8px)";
      setTimeout(() => el.remove(), 300);
    }, duration);
  },
  success(m: string): void { this.show(m, "success"); },
  error(m: string): void { this.show(m, "error"); },
  warning(m: string): void { this.show(m, "warning"); },
  info(m: string): void { this.show(m, "info"); },
};
