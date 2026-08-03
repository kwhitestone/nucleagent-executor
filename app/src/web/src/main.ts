import { createApp, type App as VueApp } from "vue";
import { createPinia } from "pinia";

import App from "./App.vue";
import router from "./router";
import i18n from "./i18n";
// Aurora 设计 token 必须先于 global.css 引入（global.css 内的规则依赖这些变量）。
// aurora.css 由 nucleagent-deploy/scripts/sync-design-tokens.sh 从设计稿生成，勿手改。
import "./styles/aurora.css";
import "./styles/global.css";
import "./styles/dashboard.css";

const MOUNT_ID = "executor-app";

let app: VueApp | null = null;

function mount() {
  app = createApp(App);
  app.use(createPinia());
  app.use(router);
  app.use(i18n);
  // 移除 Element Plus：设计稿全自绘，EP 样式与 Aurora 冲突。提示改用 useToast。
  app.mount(`#${MOUNT_ID}`);
}

function unmount() {
  if (app) {
    app.unmount();
    app = null;
  }
}

const w = globalThis as Record<string, unknown>;
if (w.__MICRO_APP_ENVIRONMENT__) {
  w.mount = mount;
  w.unmount = unmount;
} else {
  mount();
}
