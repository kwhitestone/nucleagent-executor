import { createRouter, createWebHistory, type RouteRecordRaw } from "vue-router";

// micro-app 子应用环境探测：被壳应用加载时 window.__MICRO_APP_ENVIRONMENT__ 为 true。
// 子应用路由 base 需对齐壳应用分配的路径（/executor），独立运行时用根路径。
const isMicroApp =
  (globalThis as Record<string, unknown>).__MICRO_APP_ENVIRONMENT__ === true;
const routerBase = isMicroApp ? "/executor" : "/";

const routes: RouteRecordRaw[] = [
  {
    path: "/",
    name: "dashboard",
    component: () => import("@/views/Dashboard.vue"),
  },
  {
    path: "/:pathMatch(.*)*",
    redirect: "/",
  },
];

const router = createRouter({
  history: createWebHistory(routerBase),
  routes,
});

export default router;
