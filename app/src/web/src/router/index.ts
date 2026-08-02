import {
  createRouter,
  createWebHistory,
  createWebHashHistory,
  type RouteRecordRaw,
} from "vue-router";

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
  history: isMicroApp ? createWebHashHistory(routerBase) : createWebHistory(routerBase),
  routes,
});

export default router;
