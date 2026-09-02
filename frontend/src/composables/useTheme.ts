import { computed, ref, watch } from 'vue'

/**
 * 主题与皮肤切换。
 * - 主题：data-theme="light" | "dark"，跟随系统偏好
 * - 皮肤：data-skin="brutal"（野兽派）| "mv"（mediavault 深青色调色板）| 空（默认）
 *   皮肤循环：默认 → brutal → mv → 默认
 * 选择持久化到 localStorage。
 */
export type ThemeMode = 'light' | 'dark'
export type SkinMode = 'default' | 'brutal' | 'mv'

const STORAGE_KEY_THEME = 'qmediasync-theme'
const STORAGE_KEY_SKIN = 'qmediasync-skin'

const systemDark = window.matchMedia?.('(prefers-color-scheme: dark)')?.matches ?? false

const storedTheme = localStorage.getItem(STORAGE_KEY_THEME) as ThemeMode | null
const storedSkin = localStorage.getItem(STORAGE_KEY_SKIN) as SkinMode | null

const theme = ref<ThemeMode>(storedTheme ?? (systemDark ? 'dark' : 'light'))
// 未记录过皮肤偏好时默认 mediavault 风格（前端按 mediavault 复刻）
const skin = ref<SkinMode>(storedSkin ?? 'mv')

const applyTheme = () => {
  document.documentElement.setAttribute('data-theme', theme.value)
}

const applySkin = () => {
  if (skin.value === 'brutal' || skin.value === 'mv') {
    document.documentElement.setAttribute('data-skin', skin.value)
  } else {
    document.documentElement.removeAttribute('data-skin')
  }
}

// mv 皮肤是深色系：强制深色主题，避免浅色变量干扰色板
if (skin.value === 'mv') {
  theme.value = 'dark'
}

applyTheme()
applySkin()

const toggleTheme = () => {
  theme.value = theme.value === 'light' ? 'dark' : 'light'
}

const SKIN_ORDER: SkinMode[] = ['default', 'brutal', 'mv']
const toggleSkin = () => {
  const idx = SKIN_ORDER.indexOf(skin.value)
  skin.value = SKIN_ORDER[(idx + 1) % SKIN_ORDER.length]
  if (skin.value === 'mv') {
    theme.value = 'dark'
  }
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
const isMv = computed(() => skin.value === 'mv')

export function useTheme() {
  return {
    theme,
    skin,
    isDark,
    isBrutal,
    isMv,
    toggleTheme,
    toggleSkin,
  }
}