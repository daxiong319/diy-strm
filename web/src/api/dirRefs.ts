import { http } from "./client";

/**
 * 目录配置防呆提示（T32）的接口层。
 *
 * 后端只回答「这个目录被哪些配置覆盖」，**文案在后端算好** ——
 * 前端自己拼等于把判定复制一份，两端迟早分叉，而分叉的症状是
 * 「界面说在媒体库内，Emby 里就是没有」。所以这里没有任何判断逻辑，
 * 只有一次 GET 和它的类型。
 */

/** 提示的严重程度。后端给的，前端只按它选颜色。 */
export type DirHintTone = "ok" | "warn";

export interface DirHint {
  text: string;
  tone: DirHintTone;
}

/** 后端 cloudref.DirReferences 的镜像。字段是 PascalCase —— 那边没加 json tag。 */
export interface DirReferences {
  InLibrary: boolean;
  LibraryName: string;
  IsMonitorSource: boolean;
  /** 主提示：优先给会静默失败的那条。没有提示时是空串。 */
  Hint: string;
  Tone: DirHintTone | "";
  Hints: DirHint[] | null;
}

/** 这次判断用到了哪些配置 —— 给「凭什么这么判」的展开区用。 */
export interface DirRefSnapshot {
  library_roots: string[];
  monitor_sources: string[];
  emby_locations: string[];
  /** 边界说明。诚实边界的一部分，必须展示，不能藏。 */
  notes: string[];
}

export interface DirRefResult {
  /** 归一后的路径（`/media/影视/` → `/media/影视`）。不可用时是空串。 */
  path: string;
  references: DirReferences;
  refs: DirRefSnapshot;
  /**
   * 有没有读到配置。
   *
   * false 时 `Hint` 一定是空串，界面必须**什么都不显示**：没配过媒体库根时
   * 说一句「不在媒体库内」是凭空捏造，用户会跑去设置里找一个自己明明配过
   * （或压根不存在）的东西。
   */
  configured: boolean;
}

/**
 * 查一个目录被哪些配置覆盖。
 *
 * 路径为空或不可用时后端返回 200 + 零值结果（不是 400）：用户在输入框里
 * 敲第一个字符的瞬间就会带一个半截路径过来，报错只会让界面闪一个红框。
 */
export function fetchDirReferences(path: string) {
  return http.get<DirRefResult>("/admin/dir-refs", { path });
}