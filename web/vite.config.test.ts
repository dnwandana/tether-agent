import { describe, expect, it } from "vitest"
import config from "./vite.config"

describe("vite config", () => {
  it("builds into the embedded dist folder", () => {
    expect(config.base).toBe("/")
    expect(config.build?.outDir).toBe("../internal/dashboard/dist")
    expect(config.build?.emptyOutDir).toBe(true)
    expect(config.build?.assetsDir).toBe("assets")
    expect(config.build?.sourcemap).toBe(false)
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
})
