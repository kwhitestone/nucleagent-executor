import { createApp, type App as VueApp } from "vue";
import { createPinia } from "pinia";
import ElementPlus from "element-plus";
import "element-plus/dist/index.css";

import App from "./App.vue";
import router from "./router";
import i18n from "./i18n";
import "./styles/global.css";
import "./styles/dashboard.css";

// micro-app 子应用：被壳应用加载时，挂载点可能是 #app（独立）或 #app 子节点（沙箱内）。
// micro-app 默认用 #app 容器，独立运行也用 #app，所以这里统一即可。
let app: VueApp | null = null;

function mount(el?: Element) {
  app = createApp(App);
  app.use(createPinia());
  app.use(router);
  app.use(i18n);
  app.use(ElementPlus);
  app.mount(el ?? "#app");
}

function unmount() {
  if (app) {
    app.unmount();
    app = null;
  }
}

// micro-app 生命周期：壳应用卸载子应用时调用 unmount，需挂到 window 供 micro-app 调度。
const w = globalThis as Record<string, unknown>;
if (w.__MICRO_APP_ENVIRONMENT__) {
  // micro-app 会按 window[${appName}] 寻找 mount/unmount；这里兜底用通用名。
  w.mount = () => mount();
  w.unmount = unmount;
} else {
  // 独立运行：直接挂载。
  mount();
}
