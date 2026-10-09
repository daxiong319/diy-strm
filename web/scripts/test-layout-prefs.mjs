// 布局偏好的纯函数行为（T13 · F-2/F-3）。
//
// 编排的核心只有三件事：排序、隐藏、不可隐藏。本文件把它们钉住，
// 尤其钉住「不可隐藏」—— 它是一条产品规则，不是界面上的一个禁用态，
// 任何绕过按钮的路径（localStorage 旧数据、手改存储、未来的导入）
// 都必须同样拦住。
//
// 跑法：npm run test:layout

import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import ts from "typescript";

const { outputText } = ts.transpileModule(
  readFileSync(new URL("../src/composables/layoutPrefs.ts", import.meta.url), "utf8"),
  { compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022 } },
);
const mod = { exports: {} };
const load = new Function("exports", "require", "module", outputText);
load(mod.exports, () => {
  throw new Error("layoutPrefs 不应该有运行时依赖");
}, mod);
const lp = mod.exports;

const nav = [
  { key: "dashboard", label: "仪表盘" },
  { key: "accounts", label: "存储管理" },
  { key: "settings", label: "系统设置" },
  { key: "tasks", label: "任务管理" },
  { key: "mcp", label: "MCP 服务" },
];
const keys = (items) => items.map((i) => i.key);

// 排序
assert.deepEqual(keys(lp.applyOrder(nav, [])), keys(nav), "空偏好不应改变顺序");
assert.deepEqual(
  keys(lp.applyOrder(nav, ["tasks", "accounts", "dashboard", "settings", "mcp"])),
  ["tasks", "accounts", "dashboard", "settings", "mcp"],
  "按偏好重排失败",
);
// 新菜单追加末尾，而不是插回原注册位置
assert.deepEqual(
  keys(lp.applyOrder(nav, ["tasks", "dashboard"])),
  ["tasks", "dashboard", "accounts", "settings", "mcp"],
  "未排序的菜单应按原顺序追加到末尾",
);
// 已下线的 key 不能让重排崩掉
assert.deepEqual(
  keys(lp.applyOrder(nav, ["gone", "tasks", "dashboard"])),
  ["tasks", "dashboard", "accounts", "settings", "mcp"],
  "失效 key 应当被跳过而不是崩溃",
);
// order 里重复的 key 只保留一次
assert.deepEqual(
  keys(lp.applyOrder(nav, ["tasks", "tasks", "dashboard"])),
  ["tasks", "dashboard", "accounts", "settings", "mcp"],
  "order 里重复的 key 只该出现一次",
);

// 隐藏
assert.deepEqual(keys(lp.applyHidden(nav, ["accounts"])), ["dashboard", "settings", "tasks", "mcp"], "隐藏失败");

// 不可隐藏：三条产品规则
for (const locked of ["dashboard", "mcp", "assistant"]) {
  assert.ok(!lp.canHide(locked), `${locked} 不该可隐藏`);
  assert.equal(lp.isHidden(locked, [locked]), false, `${locked} 即使在 hidden 表里也必须显示`);
  assert.deepEqual(
    keys(lp.applyHidden(nav, [locked])),
    keys(nav),
    `${locked} 即使被写进 hidden 也必须显示`,
  );
  assert.deepEqual(lp.setHidden(["accounts"], locked, true), ["accounts"], `${locked} 不许被隐藏`);
}
assert.ok(lp.canHide("tasks"), "普通菜单应当可隐藏");

// setHidden 的开合
assert.deepEqual(lp.setHidden([], "accounts", true), ["accounts"], "隐藏应写入");
assert.deepEqual(lp.setHidden(["accounts"], "accounts", true), ["accounts"], "重复隐藏不应产生重复项");
assert.deepEqual(lp.setHidden(["accounts"], "accounts", false), [], "取消隐藏应移除");

// moveItem
const moved = lp.moveItem(["a", "b", "c"], 0, 2);
assert.deepEqual(moved, ["b", "c", "a"], "前移失败");
assert.deepEqual(lp.moveItem(["a", "b", "c"], 0, 0), ["a", "b", "c"], "原地不动应返回原数组");
assert.deepEqual(lp.moveItem(["a", "b", "c"], 5, 0), ["a", "b", "c"], "越界起点应原样返回");
assert.deepEqual(lp.moveItem(["a", "b", "c"], 0, 99), ["b", "c", "a"], "越界终点应收敛到末尾");

// 存储解析：坏数据必须被丢弃而不是让后台白屏
const fakeStore = (() => {
  const m = new Map();
  globalThis.localStorage = {
    getItem: (k) => (m.has(k) ? m.get(k) : null),
    setItem: (k, v) => m.set(k, String(v)),
    removeItem: (k) => m.delete(k),
  };
  return m;
})();

fakeStore.set("litepan-admin-nav-order", "{不是 JSON");
assert.deepEqual(lp.loadLayoutPrefs(), { order: [], hidden: [], groups: [] }, "坏 JSON 应当被丢弃");

fakeStore.set("litepan-admin-nav-order", JSON.stringify(["a", 1, null, "b"]));
assert.deepEqual(lp.loadLayoutPrefs().order, ["a", "b"], "非字符串成员应被过滤");

fakeStore.set("litepan-admin-nav-groups", JSON.stringify([{ id: "g1", title: "常用" }, { id: 5 }, null]));
assert.deepEqual(lp.loadLayoutPrefs().groups, [{ id: "g1", title: "常用" }], "坏分组应被过滤");

// 往返：存进去再读出来必须一致（刷新后编排保持的底层保证）
const prefs = { order: ["tasks", "dashboard"], hidden: ["accounts"], groups: [{ id: "g1", title: "常用" }] };
lp.saveLayoutPrefs(prefs);
assert.deepEqual(lp.loadLayoutPrefs(), prefs, "编排未能持久化往返");

lp.clearLayoutPrefs();
assert.deepEqual(lp.loadLayoutPrefs(), { order: [], hidden: [], groups: [] }, "清空后应回到默认");

console.log("✅ layoutPrefs 全部断言通过");