import { ref } from "vue";

export type ThemePref = "light" | "dark" | "auto";
export type SkinPref = "default" | "brutal" | "paper";

/**
 * 全部内置皮肤。系统设置页的选择项由这份清单驱动，
 * 不要在别处再写一份 skin id 的数组 —— 两份清单漂移的表现是
 * 「选得到但套不上」（applySkin 写了个没人认的 data-skin 值，界面没反应）。
 */
export const SKIN_IDS: SkinPref[] = ["default", "brutal", "paper"];

export function isSkinPref(v: string): v is SkinPref {
  return (SKIN_IDS as string[]).includes(v);
}

const KEY = "litepan_theme";
const KEY_SKIN = "litepan_skin";
const THEME_ORDER: ThemePref[] = ["auto", "light", "dark"];

const THEME_LABELS: Record<ThemePref, string> = {
  auto: "跟随系统",
  light: "浅色主题",
  dark: "深色主题",
};

let mediaQuery: MediaQueryList | null = null;

function readSkinPref(): SkinPref {
  const v = localStorage.getItem(KEY_SKIN) ?? "";
  // 读到不认识的 skin id（手改过 localStorage，或旧版本残留）
  // 回落默认，而不是把一个没人认的值写进 data-skin。
  return isSkinPref(v) ? v : "default";
}

const skinState = ref<SkinPref>(readSkinPref());

export function supportsThemeToggle(): boolean {
  return skinState.value !== "brutal";
}

function resolve(pref: ThemePref): "light" | "dark" {
  if (pref === "auto") {
    return window.matchMedia("(prefers-color-scheme: dark)").matches ? "dark" : "light";
  }
  return pref;
}

function activeSkin(): SkinPref {
  if (typeof document === "undefined") {
    return skinState.value;
  }
  const v = document.documentElement.dataset.skin ?? "";
  return isSkinPref(v) ? v : "default";
}

function applyThemeDataset(pref: ThemePref): void {
  if (activeSkin() === "brutal") {
    document.documentElement.dataset.theme = "light";
    return;
  }
  document.documentElement.dataset.theme = resolve(pref);
}

function bindSystemThemeListener(): void {
  if (typeof window === "undefined" || !window.matchMedia || mediaQuery) return;
  mediaQuery = window.matchMedia("(prefers-color-scheme: dark)");
  mediaQuery.addEventListener("change", () => {
    if (getThemePref() === "auto" && activeSkin() !== "brutal") {
      applyThemeDataset("auto");
    }
  });
}

export function isValidThemePref(v: string): v is ThemePref {
  return v === "light" || v === "dark" || v === "auto";
}

export function getThemeLabel(pref: ThemePref): string {
  return THEME_LABELS[pref];
}

export function getThemeToggleTitle(pref: ThemePref): string {
  return `当前：${getThemeLabel(pref)}，点击切换主题`;
}

export function getNextThemePref(pref: ThemePref): ThemePref {
  const index = THEME_ORDER.indexOf(pref);
  return THEME_ORDER[(index + 1) % THEME_ORDER.length] ?? "light";
}

export function getThemePref(): ThemePref {
  const v = localStorage.getItem(KEY) ?? "";
  return isValidThemePref(v) ? v : "light";
}

export function setThemePref(pref: ThemePref): void {
  localStorage.setItem(KEY, pref);
  applyThemeDataset(pref);
}

export function applySkin(skin: SkinPref): void {
  document.documentElement.dataset.skin = skin;
  applyThemeDataset(getThemePref());
}

export function getSkinPref(): SkinPref {
  return skinState.value;
}

export function setSkinPref(skin: SkinPref): void {
  localStorage.setItem(KEY_SKIN, skin);
  skinState.value = skin;
  applySkin(skin);
}

export function previewSkin(skin: SkinPref): void {
  applySkin(skin);
}

export function restoreSavedSkin(): void {
  applySkin(skinState.value);
}

export function initTheme(): void {
  skinState.value = readSkinPref();
  applySkin(skinState.value);
  bindSystemThemeListener();
}
