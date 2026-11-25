import { readFileSync } from "node:fs"
import { join } from "node:path"
import { describe, expect, it } from "vitest"
import config from "./vite.config"

describe("vite config", () => {
  it("builds into the embedded dist folder", () => {
    expect(config.base).toBe("/")
    expect(config.build?.outDir).toBe("../internal/dashboard/dist")
    expect(config.build?.emptyOutDir).toBe(true)
    expect(config.build?.assetsDir).toBe("assets")
    expect(config.build?.sourcemap).toBe(false)
    expect(config.build?.chunkSizeWarningLimit).toBe(600)
  })

  it("runs the tests in jsdom", () => {
    expect(config.test?.environment).toBe("jsdom")
    expect(document.createElement("div").tagName).toBe("DIV")
  })

  it("sends the API calls of the dev server to the gateway", () => {
    expect(config.server?.proxy).toEqual({
      "/api": "http://127.0.0.1:8787",
      "/auth": "http://127.0.0.1:8787",
    })
  })

  it("maps @ to the src folder", () => {
    const alias = config.resolve?.alias as Record<string, string>
    expect(alias["@"]).toMatch(/\/src$/)
  })

  it("resolves an @ import in the tests", async () => {
    const m = await import("@/lib/time")
    expect(Object.keys(m).length).toBeGreaterThan(0)
  })

  it("gives TypeScript the same @ path", () => {
    const ts = JSON.parse(readFileSync(join(import.meta.dirname, "tsconfig.json"), "utf8"))
    expect(ts.compilerOptions.baseUrl).toBe(".")
    expect(ts.compilerOptions.paths).toEqual({ "@/*": ["./src/*"] })
  })
})
