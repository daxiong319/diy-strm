// 求片站入口。
//
// **独立于管理台 main.ts**：这个站构建进 `internal/api/web/request/`，
// 由 Go 侧单独在 7812（或设置的端口）上提供，跑在独立进程式监听里。
// 它不带 vue-router、不带 pinia —— 求片站只有三个界面
// （登录 / 首页 / 我的），用状态切换就够了，引入路由反而会让
// 「/login 在求片端口上」这条性质变得依赖前端配置。
//
// 状态机故意放在最显眼的地方：未登录 ⇒ 只渲染登录页，其余一律回登录页。
// 因为后端每个 /api 接口都独立校验 cookie（portalRequireSession），
// 前端这道判断只决定「先显示哪个界面」，不承担任何放行责任。

import { createApp, defineComponent, h, ref } from "vue";
import { initTheme } from "@/utils/theme";
import PortalLoginView from "@/views/portal/PortalLoginView.vue";
import PortalRequestView from "@/views/portal/PortalRequestView.vue";
import { portalApi, type PortalMe } from "@/api/request";

import "@/styles/tokens.css";
import "@/styles/base.css";
import "@/styles/buttons.css";
import "@/styles/portal.css";

initTheme();

const me = ref<PortalMe | null>(null);
const checking = ref(true);

async function refreshMe() {
  try {
    me.value = await portalApi.me();
  } catch {
    // 401 走这里很正常 —— 没登录或 cookie 过期就是这一条路径。
    me.value = null;
  } finally {
    checking.value = false;
  }
}

void refreshMe();

const App = defineComponent({
  name: "PortalApp",
  setup() {
    return () => {
      if (checking.value) {
        return h("div", { class: "portal-boot" }, "正在连接求片中心…");
      }
      if (!me.value) {
        return h(PortalLoginView, {
          onLoggedIn: (next: PortalMe) => {
            me.value = next;
          },
        });
      }
      return h(PortalRequestView, {
        me: me.value,
        onLogout: async () => {
          try {
            await portalApi.logout();
          } finally {
            // 即使注销请求失败也要退回登录页：
            // 继续留在页面上只会让人以为已经登出，其实 cookie 还在。
            me.value = null;
          }
        },
        onMeChange: (next: PortalMe) => {
          me.value = next;
        },
      });
    };
  },
});

createApp(App).mount("#app");