// @vitest-environment node
import { readFileSync } from "node:fs"
import { describe, expect, it } from "vitest"

const read = (f: string) => readFileSync(new URL("./" + f, import.meta.url), "utf8")
const pkg = JSON.parse(read("package.json")) as {
  engines: { node: string }
  dependencies: Record<string, string>
  devDependencies: Record<string, string>
}
const lock = JSON.parse(read("package-lock.json")) as { packages: Record<string, unknown> }

describe("package.json", () => {
  it("pins each package to an exact version", () => {
    for (const [name, v] of Object.entries({ ...pkg.dependencies, ...pkg.devDependencies })) {
      expect(v, name).toMatch(/^\d+\.\d+\.\d+$/)
    }
  })

  it("has the versions of the dashboard stack", () => {
    expect(pkg.dependencies).toMatchObject({
      "reka-ui": "2.11.0",
      "@vueuse/core": "14.4.0",
      "class-variance-authority": "0.7.1",
      cn: "0.5.2",
      "@lucide/vue": "1.70.0",
      "vue-sonner": "2.0.9",
      vue: "3.5.17",
      "vue-router": "4.5.1",
      marked: "15.0.12",
    })
    expect(pkg.devDependencies).toMatchObject({
      tailwindcss: "4.3.5",
      "@tailwindcss/vite": "4.3.5",
      "tw-animate-css": "1.4.0",
      "@testing-library/vue": "8.1.0",
      "@testing-library/user-event": "14.6.8",
    })
    expect(pkg.engines.node).toBe(">=24")
  })

  // make web runs npm ci. A missing native entry stops the build on that platform.
  it("keeps the native builds of the CSS tools for each release platform", () => {
    for (const p of [
      "@tailwindcss/oxide-linux-x64-gnu",
      "@tailwindcss/oxide-linux-arm64-gnu",
      "@tailwindcss/oxide-darwin-arm64",
      "lightningcss-linux-x64-gnu",
      "lightningcss-linux-arm64-gnu",
      "lightningcss-darwin-arm64",
    ]) {
      expect(lock.packages["node_modules/" + p], p).toBeDefined()
    }
  })
})

describe(".npmrc", () => {
  it("saves exact versions", () => {
    expect(
      read(".npmrc")
        .split("\n")
        .map((l) => l.trim()),
    ).toContain("save-exact=true")
  })
})
