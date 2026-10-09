<script setup lang="ts">
// 求片站登录页。
//
// **单列、窄屏优先**：这个站的主要用户是家里人用手机浏览器打开的，
// 他们在电视柜旁、在沙发上，页面不该长得像一个后台。
//
// 登录的是管理台同一套账号：管理员不需要为求片单独记一套密码。
// 后端在登录成功前额外校验「这个人有求片权限」，所以这里不需要
// （也不该）自己判断该不该放他进去 —— 少一个能漂移的第二套真相。
import { ref } from "vue";
import { getApiErrorMessage } from "@/api/client";
import { portalApi, type PortalMe } from "@/api/request";

const emit = defineEmits<{ loggedIn: [PortalMe] }>();

const username = ref("");
const password = ref("");
const submitting = ref(false);
const error = ref("");

async function submit() {
  if (submitting.value) return;
  if (!username.value.trim() || !password.value) {
    error.value = "请填写用户名和密码";
    return;
  }
  submitting.value = true;
  error.value = "";
  try {
    const res = await portalApi.login(username.value.trim(), password.value);
    // 服务端会把 DailyLimit/RequireReview/权限一并带回来，
    // 首页直接用这份结论渲染，不再问第二次。
    emit("loggedIn", res);
  } catch (e) {
    error.value = getApiErrorMessage(e, "登录失败");
  } finally {
    submitting.value = false;
  }
}
</script>

<template>
  <div class="portal-login">
    <form class="portal-login__card" @submit.prevent="submit">
      <h1 class="portal-login__title">求片中心</h1>
      <p class="portal-login__sub">想看的写在这里，我们帮你盯着资源。</p>

      <label class="portal-login__field">
        <span class="portal-login__label">用户名</span>
        <input
          v-model="username"
          type="text"
          autocomplete="username"
          autocapitalize="none"
          autocorrect="off"
          spellcheck="false"
          :disabled="submitting"
        />
      </label>

      <label class="portal-login__field">
        <span class="portal-login__label">密码</span>
        <input
          v-model="password"
          type="password"
          autocomplete="current-password"
          :disabled="submitting"
        />
      </label>

      <p v-if="error" class="portal-login__error">{{ error }}</p>

      <button class="portal-login__submit" type="submit" :disabled="submitting">
        {{ submitting ? "登录中…" : "登录" }}
      </button>

      <p class="portal-login__hint">用的是后台那套账号密码，没记起来就问管理员。</p>
    </form>
  </div>
</template>