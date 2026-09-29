import { http } from "./client";

// ---------------------------------------------------------------------------
// 影视发现 API（对齐后端 /admin/discovery/*）
// ---------------------------------------------------------------------------

export interface DiscoverItem {
  source: string;
  media_type: string;
  tmdb_id?: number;
  douban_id?: string;
  title: string;
  original_title?: string;
  poster?: string;
  backdrop?: string;
  overview?: string;
  vote_avg?: number;
  release_date?: string;
  year?: number;
  entity_key?: string;
  [key: string]: unknown;
}

export interface PageResult {
  items: DiscoverItem[];
  page: number;
  total_pages?: number;
  total_results?: number;
  [key: string]: unknown;
}

export interface CalendarEpisode {
  entity_key?: string;
  title?: string;
  show_title?: string;
  overview?: string;
  poster?: string;
  still?: string;
  air_date?: string;
  season?: number;
  episode?: number;
  tmdb_id?: number;
  [key: string]: unknown;
}

export interface CalendarDay {
  date: string;
  label?: string;
  episodes?: CalendarEpisode[];
  items?: DiscoverItem[];
  [key: string]: unknown;
}

export interface DiscoverMeta {
  genres_movie: Record<string, string>;
  genres_tv: Record<string, string>;
  providers: Array<Record<string, string>>;
  regions: Array<Record<string, string>>;
  collections: Array<Record<string, string>>;
  douban_tags: Record<string, string[]>;
  default_source: string;
  douban_category: Record<string, string[]>;
  douban_sort: Array<Record<string, string>>;
  anime_genres: string[];
  anime_regions: Array<Record<string, string>>;
  anime_sort: Array<Record<string, string>>;
  maoyan_category: Array<{ key?: string; label?: string; value?: string }>;
}

export interface DiscoveryFavorite {
  id: number;
  entity_key: string;
  source: string;
  media_type: string;
  external_id: string;
  tmdb_id?: number;
  title: string;
  original_title?: string;
  poster?: string;
  overview?: string;
  vote_avg?: number;
  year?: number;
  created_at?: string;
}

export interface RankingItem extends DiscoverItem {
  rank?: number;
  heat?: number | string;
  [key: string]: unknown;
}

export interface RankingGroup {
  key?: string;
  label?: string;
  items?: RankingItem[];
  [key: string]: unknown;
}

export function fetchDiscoverMeta() {
  return http.get<DiscoverMeta>("/admin/discovery/meta");
}

export function fetchDiscoverExplore(params: {
  type?: string;
  genre?: string;
  year?: string;
  region?: string;
  sort_by?: string;
  page?: number;
  force?: boolean;
}) {
  return http.get<PageResult>("/admin/discovery/explore", params as Record<string, string | number | boolean | undefined>);
}

export function fetchDiscoverExploreDouban(params: { type?: string; tag?: string; page?: number; force?: boolean }) {
  return http.get<PageResult>("/admin/discovery/explore/douban", params as Record<string, string | number | boolean | undefined>);
}

export function fetchDiscoverRankings(params: {
  provider?: string;
  region?: string;
  media_type?: string;
  page?: number;
  force?: boolean;
}) {
  return http.get<PageResult>("/admin/discovery/rankings", params as Record<string, string | number | boolean | undefined>);
}

export function fetchDiscoverRankingsMaoyan(params: { category?: string; force?: boolean }) {
  return http.get<{ groups?: RankingGroup[]; feed_status?: string }>(
    "/admin/discovery/rankings/maoyan",
    params as Record<string, string | number | boolean | undefined>,
  );
}

export function fetchDiscoverCalendar(params: { days?: number | string; kind?: string; force?: boolean }) {
  return http.get<CalendarDay[]>("/admin/discovery/calendar", params as Record<string, string | number | boolean | undefined>);
}

export function fetchDiscoverAnimeCalendar(params: { force?: boolean }) {
  return http.get<CalendarDay[]>("/admin/discovery/anime/calendar", params as Record<string, string | number | boolean | undefined>);
}

export function fetchDiscoverAnimeSearch(params: { keyword?: string; source?: string; page?: number; force?: boolean }) {
  return http.get<PageResult>("/admin/discovery/anime/search", params as Record<string, string | number | boolean | undefined>);
}

export function fetchDiscoverActors(params: { page?: number; force?: boolean }) {
  return http.get<PageResult>("/admin/discovery/actors", params as Record<string, string | number | boolean | undefined>);
}

export function fetchDiscoverActorWorks(id: number | string) {
  return http.get<{ works?: DiscoverItem[]; items?: DiscoverItem[] }>(`/admin/discovery/actors/${id}/works`);
}

export function fetchDiscoverSearch(params: { q?: string; media_type?: string; page?: number; force?: boolean }) {
  return http.get<PageResult>("/admin/discovery/search", params as Record<string, string | number | boolean | undefined>);
}

export function fetchDiscoverDetails(source: string, type: string, id: string) {
  return http.get<Record<string, unknown>>(`/admin/discovery/details/${source}/${type}/${id}`);
}

export function fetchDiscoverFavorites() {
  return http.get<{ items: DiscoveryFavorite[] }>("/admin/discovery/favorites");
}

export function addDiscoverFavorite(body: Partial<DiscoveryFavorite> & { entity_key: string }) {
  return http.post<{ ok: boolean }>("/admin/discovery/favorites", body);
}

export function deleteDiscoverFavorite(id: number) {
  return http.del<{ ok: boolean }>(`/admin/discovery/favorites/${id}`);
}

export function checkDiscoverFavorites(keys: string[]) {
  return http.post<{ favorited: Record<string, boolean> }>("/admin/discovery/favorites/check", { keys });
}

export function fetchDiscoverSettings() {
  return http.get<Record<string, unknown>>("/admin/discovery/settings");
}

export function updateDiscoverSettings(values: Record<string, unknown>) {
  return http.put<Record<string, unknown>>("/admin/discovery/settings", values);
}
