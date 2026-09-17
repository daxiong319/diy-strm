import type { AxiosInstance } from 'axios'

/**
 * TMDB 影视搜索辅助。
 *
 * 各页面（云盘频道订阅、影巢搜索/订阅、影视订阅等）都通过 `/scrape/tmdb-search`
 * 同时查询电影和电视剧。TMDB 的 `query` 会把标题里的年份当成片名的一部分
 * （如 `交锋 (2026)` 会查无结果），因此必须先剥离年份，再通过 `year` 参数精确匹配。
 *
 * HTTP 客户端由调用方以组件内 `useHttpClient()` 取得的实例传入，保持依赖注入契约。
 */

export interface TmdbSearchItem {
  tmdb_id: number
  title: string
  original_title: string
  year: number
  poster_url: string
  overview: string
  vote_average: number
  media_type: 'movie' | 'tvshow'
  seasons?: { season_number: number; name: string; episode_count: number; air_date: string }[]
  total_seasons?: number
  total_episodes?: number
}

// 与后端 `requests.TMDBSearchRequest` 的年份校验区间一致
const MIN_YEAR = 1900
const MAX_YEAR = 2100

/**
 * 把用户输入拆成片名和年份。
 *
 * 支持 `交锋 (2026)`、`交锋（2026）`、`交锋 2026` 等写法。年份超出后端校验区间时
 * 视为片名的一部分，不做拆分，避免把片名里的数字误判为年份。
 */
export function parseTmdbQuery(raw: string): { name: string; year: number } {
  const input = (raw || '').trim()
  if (!input) return { name: '', year: 0 }

  let name = input
  let year = 0

  // 优先匹配括号内的 4 位年份，中英文括号均可
  const bracket = name.match(/^(.+?)\s*[（(]\s*(\d{4})\s*[)）]\s*$/)
  if (bracket) {
    name = bracket[1]
    year = Number(bracket[2])
  } else {
    // 退化匹配结尾的裸年份，如「交锋 2026」
    const bare = name.match(/^(.+?)\s+(\d{4})$/)
    if (bare) {
      name = bare[1]
      year = Number(bare[2])
    }
  }

  if (year < MIN_YEAR || year > MAX_YEAR) {
    return { name: input, year: 0 }
  }
  const trimmed = name.trim()
  return { name: trimmed || input, year }
}

/** 单次 TMDB 搜索；未命中和请求失败都返回空数组，由调用方决定提示文案。 */
async function queryTmdb(
  http: AxiosInstance,
  name: string,
  type: 'movie' | 'tvshow',
  year: number,
): Promise<TmdbSearchItem[]> {
  const params: Record<string, string | number> = { name, type }
  if (year > 0) params.year = year
  const resp = await http.get('/api/scrape/tmdb-search', { params })
  if (resp.data?.code === 200 && Array.isArray(resp.data.data)) {
    return (resp.data.data as Omit<TmdbSearchItem, 'media_type'>[]).map((d) => ({
      ...d,
      media_type: type,
    }))
  }
  return []
}

/**
 * 搜索影视（电影 + 电视剧合并）。
 *
 * 先带年份精确查；带年份无结果时再去掉年份重查一次，避免 TMDB 年份字段与
 * 实际首播年不一致（未上映、改档或新版剧集）导致漏结果。有年份时优先展示
 * 年份最接近的结果，其次按评分排序。
 *
 * `http.get` 抛出的错误由调用方捕获，以复用各自的错误提示文案。
 */
export async function searchTmdbMulti(
  http: AxiosInstance,
  raw: string,
  options: { limit?: number } = {},
): Promise<TmdbSearchItem[]> {
  const limit = options.limit && options.limit > 0 ? options.limit : 12
  const { name, year } = parseTmdbQuery(raw)
  if (!name) return []

  const searchOnce = async (withYear: boolean) => {
    const y = withYear ? year : 0
    const [movieItems, tvItems] = await Promise.all([
      queryTmdb(http, name, 'movie', y),
      queryTmdb(http, name, 'tvshow', y),
    ])
    return [...movieItems, ...tvItems]
  }

  let items = await searchOnce(year > 0)
  if (year > 0 && items.length === 0) {
    items = await searchOnce(false)
  }
  if (!items.length) return []

  items.sort((a, b) => {
    if (year > 0) {
      const da = a.year ? Math.abs(a.year - year) : Number.MAX_SAFE_INTEGER
      const db = b.year ? Math.abs(b.year - year) : Number.MAX_SAFE_INTEGER
      if (da !== db) return da - db
    }
    return (b.vote_average || 0) - (a.vote_average || 0)
  })
  return items.slice(0, limit)
}

/**
 * 按 TMDB ID 获取剧集详情（含季列表和总集数）；失败时返回 `null`。
 *
 * 后端详情分支返回的字段名是 `number_of_episodes`，这里统一归一化到
 * `TmdbSearchItem.total_episodes`，避免调用方直接读后端原始字段。
 */
export async function fetchTmdbTvDetail(
  http: AxiosInstance,
  tmdbId: number,
): Promise<TmdbSearchItem | null> {
  if (!tmdbId) return null
  try {
    const resp = await http.get('/api/scrape/tmdb-search', {
      params: { type: 'tvshow', tmdb_id: tmdbId },
    })
    if (resp.data?.code === 200 && resp.data.data?.length) {
      const detail = resp.data.data[0]
      return {
        ...detail,
        media_type: 'tvshow',
        seasons: detail.seasons || [],
        total_seasons: (detail.seasons || []).length,
        total_episodes: detail.total_episodes || detail.number_of_episodes || 0,
      }
    }
  } catch {
    /* 详情获取失败时由调用方回退到搜索结果 */
  }
  return null
}
