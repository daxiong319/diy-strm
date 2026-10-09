import { createRouter, createWebHistory, type RouteRecordRaw } from "vue-router";
import { fetchAuthStatus } from "@/api/auth";
import { useAuthStore } from "@/stores/auth";

const routes: RouteRecordRaw[] = [
  {
    path: "/",
    name: "home",
    component: () => import("@/views/IndexView.vue"),
    meta: { title: "文件浏览" },
  },
  {
    path: "/login",
    name: "login",
    component: () => import("@/views/LoginView.vue"),
    meta: { title: "管理员登录", guestOnly: true },
  },
  {
    path: "/admin",
    name: "admin",
    component: () => import("@/views/AdminView.vue"),
    meta: { title: "管理后台", requiresAuth: true },
  },
  {
    // 免登录分享页。刻意放在 404 兜底之前、且**不带**任何 meta.title 之外的守卫：
    // 访客没有会话，走 requiresAuth 会把他踢去 /login，页面上只剩一句「请登录」——
    // 那就等于没有分享页。
    path: "/share/:code",
    name: "library-share",
    component: () => import("@/views/share/SharePage.vue"),
    meta: { title: "分享" },
  },
  { path: "/:pathMatch(.*)*", redirect: "/" },
];

export const router = createRouter({
  history: createWebHistory(),
  routes,
});

router.beforeEach(async (to) => {
  if (to.meta.title) {
    document.title = `${String(to.meta.title)} - diy-strm`;
  }

  const auth = useAuthStore();

  if (to.meta.requiresAuth) {
    if (auth.loaded && auth.sessionAdmin) {
      return true;
    }
    try {
      const status = await fetchAuthStatus();
      if (!status.is_admin) {
        return { path: "/login", query: { redirect: to.fullPath } };
      }
      auth.applyStatus(status);
      return true;
    } catch {
      return { path: "/login", query: { redirect: to.fullPath } };
    }
  }

  if (to.meta.guestOnly) {
    if (auth.loaded && auth.sessionAdmin) return "/admin";
    try {
      const status = await fetchAuthStatus();
      if (status.is_admin) {
        auth.applyStatus(status);
        return "/admin";
      }
    } catch {
      /* 未登录，继续访问登录页 */
    }
  }

  if (to.name === "home") {
    if (auth.loaded && auth.sessionAdmin) return true;
    try {
      const status = await fetchAuthStatus();
      auth.applyStatus(status);
      if (!status.public_index_enabled && !status.is_admin) {
        return { path: "/login", query: { redirect: to.fullPath } };
      }
    } catch {
      /* 网络异常时仍允许访问首页 */
    }
  }

  return true;
});
