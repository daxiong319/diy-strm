/**
 * T13 · 功能直达（⌘K）的过滤逻辑。
 *
 * 与 test-layout-prefs.mjs 同样的做法：把 TS 源码用 typescript 的
 * transpileModule 当场转成 JS 再跑，不引测试框架。理由是这个仓库的
 * 前端没有单测基础设施，为一个纯函数拉一整套进来不值得。
 *
 * 这里测的是「搜什么能搜到」这一层 —— ⌘K 全部的价值都在这上面。
 */
import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { dirname, resolve } from "node:path";
import ts from "typescript";

const here = dirname(fileURLToPath(import.meta.url));
const src = readFileSync(resolve(here, "../src/composables/useCommandPalette.ts"), "utf8");

const js = ts.transpileModule(src, {
  compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022 },
}).outputText;

// 只把被测的纯函数拿出来跑。vue / API 层用最小 stub：
// 模块顶层有一批 ref()，不给它就执行不过去，而那些状态与过滤逻辑无关。
// 其余依赖一律抛错 —— 顺带证明被测逻辑没有偷偷依赖别的东西。
const modules = {
  vue: {
    ref: (v) => ({ value: v }),
    shallowRef: (v) => ({ value: v }),
    computed: (fn) => ({ get value() { return fn(); } }),
    watch: () => {},
    onBeforeUnmount: () => {},
  },
  "vue-router": { useRouter: () => ({ push: () => Promise.resolve() }) },
  "@/api/settings": { fetchSettingsIndex: () => Promise.reject(new Error("测试里不应发请求")) },
};
const exported = {};
new Function("require", "exports", js)((name) => {
  if (!(name in modules)) throw new Error("被测逻辑不应依赖 " + name);
  return modules[name];
}, exported);
const { filterPaletteItems } = exported;

let failed = 0;
function check(name, cond) {
  if (cond) {
    console.log("  ✓ " + name);
  } else {
    failed++;
    console.error("  ✗ " + name);
  }
}

const items = [
  {
    key: "strm_token",
    label: "STRM 播放令牌",
    category: "strm",
    category_label: "STRM 设置",
    keywords: ["strm", "token", "strm_token", "str m 播放令牌", "str 设置"],
  },
  {
    key: "cache_ttl",
    label: "全局缓存时间",
    category: "performance",
    category_label: "性能设置",
    keywords: ["cache", "ttl", "cache_ttl", "全局缓存时间", "性能设置"],
  },
  {
    key: "log_level",
    label: "日志级别",
    category: "system",
    category_label: "系统设置",
    keywords: ["log", "level", "log_level", "日志级别", "系统设置"],
  },
];

console.log("· 空查询");
{
  const r = filterPaletteItems(items, "");
  check("给出一批而不是全部", r.length > 0 && r.length <= 12);
}

console.log("· 中文标签能搜到");
check("日志级别", filterPaletteItems(items, "日志级别").some((x) => x.key === "log_level"));

console.log("· 标签前缀能搜到");
check("缓存", filterPaletteItems(items, "缓存").some((x) => x.key === "cache_ttl"));

console.log("· 裸 key 能搜到");
check("strm_token", filterPaletteItems(items, "strm_token").some((x) => x.key === "strm_token"));

console.log("· 拆开的 key 片段能搜到");
check("ttl", filterPaletteItems(items, "ttl").some((x) => x.key === "cache_ttl"));

console.log("· 多词是「全部命中」而不是「任一命中」");
{
  const r = filterPaletteItems(items, "缓存 令牌").map((x) => x.key);
  check("不会把两个不同设置都算命中", r.length === 0);
  check("带空格的两个词能同时命中一个", filterPaletteItems(items, "缓存 时间").length === 1);
}

console.log("· 标签命中排在关键词命中前面");
{
  const r = filterPaletteItems(items, "级别").map((x) => x.key);
  check("唯一结果就是日志级别", r.length === 1 && r[0] === "log_level");
}

console.log("· 大小写不敏感");
check("LOG_LEVEL", filterPaletteItems(items, "LOG_LEVEL").length === 1);
check("Log", filterPaletteItems(items, "Log").some((x) => x.key === "log_level"));

console.log("· 搜不到就是空，不硬凑");
check("不存在的词返回空", filterPaletteItems(items, "zzzz不存在").length === 0);

console.log("· 空白查询等同空查询");
check("只有空格", filterPaletteItems(items, "   ").length === filterPaletteItems(items, "").length);

console.log(failed ? `\n❌ ${failed} 条断言失败` : "\n✅ 全部断言通过");
process.exit(failed ? 1 : 0);