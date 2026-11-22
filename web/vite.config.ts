import type { UserConfig } from "vitest/config"

// Vitest loads this file again inside the jsdom test, where esbuild stops with a realm error.
// Only Vite needs the Vue plugin, so the plugin loads only when no DOM exists.
const plugins =
  typeof document === "undefined" ? [(await import("@vitejs/plugin-vue")).default()] : []

const config = {
  plugins,
  base: "/",
  build: {
    outDir: "../internal/dashboard/dist",
    // The output folder is outside the Vite root. Vite keeps old files there without this flag.
    emptyOutDir: true,
    assetsDir: "assets",
    sourcemap: false,
    target: "es2022",
    modulePreload: { polyfill: false },
    reportCompressedSize: false,
  },
  server: {
    proxy: {
      "/api": "http://127.0.0.1:8787",
      "/auth": "http://127.0.0.1:8787",
    },
  },
  test: {
    environment: "jsdom",
    setupFiles: ["./vitest.setup.ts"],
    include: ["**/*.test.ts"],
    exclude: ["node_modules/**"],
  },
} satisfies UserConfig

export default config
