import { createApp, type App as VueApp } from "vue";
import { createPinia } from "pinia";
import ElementPlus from "element-plus";
import "element-plus/dist/index.css";

import App from "./App.vue";
import router from "./router";
import i18n from "./i18n";
import "./styles/global.css";
import "./styles/dashboard.css";

const MOUNT_ID = "executor-app";

let app: VueApp | null = null;

function mount() {
  app = createApp(App);
  app.use(createPinia());
  app.use(router);
  app.use(i18n);
  app.use(ElementPlus);
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
