import { computed, ref, watch } from 'vue'

/**
 * 主题与皮肤切换。
 * - 主题：data-theme="light" | "dark"，跟随系统偏好
 * - 皮肤：data-skin="brutal"（野兽派）| 空（默认）
 * 选择持久化到 localStorage。
 */
export type ThemeMode = 'light' | 'dark'
export type SkinMode = 'default' | 'brutal'

const STORAGE_KEY_THEME = 'qmediasync-theme'
const STORAGE_KEY_SKIN = 'qmediasync-skin'

const systemDark = window.matchMedia?.('(prefers-color-scheme: dark)')?.matches ?? false

const storedTheme = localStorage.getItem(STORAGE_KEY_THEME) as ThemeMode | null
const storedSkin = localStorage.getItem(STORAGE_KEY_SKIN) as SkinMode | null

const theme = ref<ThemeMode>(storedTheme ?? (systemDark ? 'dark' : 'light'))
const skin = ref<SkinMode>(storedSkin ?? 'default')

const applyTheme = () => {
  document.documentElement.setAttribute('data-theme', theme.value)
}

const applySkin = () => {
  if (skin.value === 'brutal') {
    document.documentElement.setAttribute('data-skin', 'brutal')
  } else {
    document.documentElement.removeAttribute('data-skin')
  }
}

applyTheme()
applySkin()

const toggleTheme = () => {
  theme.value = theme.value === 'light' ? 'dark' : 'light'
}

const toggleSkin = () => {
  skin.value = skin.value === 'brutal' ? 'default' : 'brutal'
}

watch(theme, (value) => {
  localStorage.setItem(STORAGE_KEY_THEME, value)
  applyTheme()
})

watch(skin, (value) => {
  localStorage.setItem(STORAGE_KEY_SKIN, value)
  applySkin()
})

const isDark = computed(() => theme.value === 'dark')
const isBrutal = computed(() => skin.value === 'brutal')

export function useTheme() {
  return {
    theme,
    skin,
    isDark,
    isBrutal,
    toggleTheme,
    toggleSkin,
  }
}
