import { fileURLToPath, URL } from "node:url";
import { defineConfig } from "vite";
import vue from "@vitejs/plugin-vue";

// 求片站（request portal）**单独一次构建**，产物落在 web/request/。
//
// 为什么要单独构建，而不是在 vite.config.ts 里多写一个 input：
//   一次构建 = 一个 outDir = 两套产物混在同一个 assets/ 下，
//   于是「这个 chunk 属于后台还是属于求片站」只能靠文件名猜 ——
//   求片站只要多写一个静态 import 猜错，/assets/index-xxx.js 就把
//   后台主包发到求片端口上，而且没有任何报错。
//   分目录之后，后端 fs.Sub(webFS, "web/request") 拿到的目录里
//   只有求片站自己的文件，这个隔离由**目录结构**保证。
//   对应测试：internal/api/wiring_reachability_test.go 的
//   TestRequestPortalAssetsAreNotAdminAssets。
//
// vue-vendor 会被打包两份（后台一份、求片站一份）。求片站是个三页面的小应用，
// 多出来的几十 KB 换来的是上面那条结构性保证，划算。
export default defineConfig({
  plugins: [
    vue({
      template: {
        compilerOptions: {
          isCustomElement: (tag) => tag.startsWith("media-"),
        },
      },
    }),
  ],
  resolve: {
    alias: [{ find: "@", replacement: fileURLToPath(new URL("./src", import.meta.url)) }],
  },
  // 相对路径：求片站同时挂在 / 和 /login 两个路径下，
  // 绝对路径 /assets/... 虽然也能work，但相对路径让产物目录自洽
  // （目录搬走了也不用改 base）。
  base: "./",
  // publicDir: false —— public/ 里是 favicon.ico、logos/、licenses/、static/，
  // 全是后台那个站用的（求片站的图标写成了 data URI 内联在入口页里）。
  // 不关掉的话每次构建都会把几十 KB 无关资源复制进求片站产物，
  // 而这些文件最后会被 //go:embed 打进二进制里，白占体积。
  publicDir: false,
  build: {
    outDir: "../internal/api/web/request",
    emptyOutDir: true,
    // 产物入口名 = 源文件名（Vite 对 html 入口不认 rollup input 的 key，
    // 也不认你改 outDir），所以源文件必须直接叫 request.html ——
    // 放进 web/portal/ 下会被再套一层目录，变成 request/portal/request.html。
    // 后端读的常量是 portalEntryFile，两边必须一起改。
    rollupOptions: {
      input: fileURLToPath(new URL("./request.html", import.meta.url)),
      output: {
        entryFileNames: "assets/[name]-[hash].js",
        chunkFileNames: "assets/[name]-[hash].js",
        assetFileNames: "assets/[name]-[hash][extname]",
      },
    },
    chunkSizeWarningLimit: 1200,
  },
});
