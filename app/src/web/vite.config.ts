import { defineConfig, loadEnv } from "vite";
import vue from "@vitejs/plugin-vue";
import { resolve } from "node:path";

// 端口、后端地址、跨域目标全部可通过环境变量配置（.env 或 shell）：
//   EXECUTOR_WEB_PORT (默认 6698)             — dev server 端口
//   EXECUTOR_BACKEND_URL (默认 http://localhost:6690) — /api 代理目标（executor 后端）
// 支持微前端：作为 micro-app 子应用运行时，端口由壳应用编排注入。
export default defineConfig(({ mode }) => {
  const env = loadEnv(mode, process.cwd(), "");
  const port = Number(env.EXECUTOR_WEB_PORT ?? env.PORT ?? 6698);
  const backendUrl = env.EXECUTOR_BACKEND_URL ?? "http://localhost:6690";

  return {
    plugins: [vue()],
    resolve: {
      alias: {
        "@": resolve(process.cwd(), "src"),
      },
    },
    server: {
      port,
      // micro-app 子应用需要被壳应用 fetch 跨域加载，允许跨域。
      cors: true,
      proxy: {
        "/api": {
          target: backendUrl,
          changeOrigin: true,
        },
      },
    },
  };
});
