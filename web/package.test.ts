// @vitest-environment node
import { readFileSync } from "node:fs"
import { describe, expect, it } from "vitest"

const read = (f: string) => readFileSync(new URL("./" + f, import.meta.url), "utf8")
type Pkg = {
  engines: { node: string; npm?: string }
  dependencies: Record<string, string>
  devDependencies: Record<string, string>
  allowScripts?: Record<string, boolean>
  overrides?: Record<string, unknown>
}
type LockEntry = { name?: string; hasInstallScript?: boolean; inBundle?: boolean; link?: boolean }

const pkg = JSON.parse(read("package.json")) as Pkg
const lock = JSON.parse(read("package-lock.json")) as { packages: Record<string, LockEntry> }

// npm matches the registry name, and it never runs the scripts of a bundled or linked entry.
function installScriptNames(packages: Record<string, LockEntry>): string[] {
  const names = new Set<string>()
  for (const [key, entry] of Object.entries(packages)) {
    if (key === "" || !entry.hasInstallScript || entry.inBundle || entry.link) continue
    // A key with no node_modules/ is a local folder. Its path names the entry.
    const at = key.lastIndexOf("node_modules/")
    names.add(entry.name ?? (at === -1 ? key : key.slice(at + "node_modules/".length)))
  }
  return [...names].sort()
}

describe("package.json", () => {
  it("pins each package to an exact version", () => {
    for (const [name, v] of Object.entries({ ...pkg.dependencies, ...pkg.devDependencies })) {
      expect(v, name).toMatch(/^\d+\.\d+\.\d+$/)
    }
  })

  it("pins each override to an exact version", () => {
    expect(pkg.overrides).toEqual({ "js-beautify": { glob: "14.0.2", minimatch: "11.0.1" } })
    const leaves = (v: unknown, at: string): [string, string][] =>
      typeof v === "string"
        ? [[at, v]]
        : typeof v === "object" && v !== null
          ? Object.entries(v).flatMap(([k, c]) => leaves(c, at + "." + k))
          : []
    for (const [at, v] of leaves(pkg.overrides, "overrides")) {
      expect(v, at).toMatch(/^\d+\.\d+\.\d+$/)
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
      vue: "3.5.45",
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
    expect(pkg.engines).toEqual({ node: "^24.18.0 || >=26.3.0", npm: ">=11.16.0" })
  })

  // make web runs npm ci, and allowScripts denies the esbuild script. So esbuild cannot download a
  // missing binary. A missing native entry stops the build on that platform.
  it("keeps the native builds of the build tools for each release platform", () => {
    for (const p of [
      "@tailwindcss/oxide-linux-x64-gnu",
      "@tailwindcss/oxide-linux-arm64-gnu",
      "@tailwindcss/oxide-darwin-arm64",
      "lightningcss-linux-x64-gnu",
      "lightningcss-linux-arm64-gnu",
      "lightningcss-darwin-arm64",
      "@esbuild/linux-x64",
      "@esbuild/linux-arm64",
      "@esbuild/darwin-arm64",
      "@rollup/rollup-linux-x64-gnu",
      "@rollup/rollup-linux-arm64-gnu",
      "@rollup/rollup-darwin-arm64",
    ]) {
      expect(lock.packages["node_modules/" + p], p).toBeDefined()
    }
  })

  it("finds the registry name of each install script in a lock file", () => {
    const packages: Record<string, LockEntry> = {
      "": { name: "tether-dashboard", hasInstallScript: true },
      "node_modules/@scope/a": { hasInstallScript: true },
      "node_modules/x": {},
      "node_modules/x/node_modules/b": { hasInstallScript: true },
      "node_modules/y/node_modules/b": { hasInstallScript: true },
      "node_modules/alias": { name: "real", hasInstallScript: true },
      "node_modules/bundled": { hasInstallScript: true, inBundle: true },
      "node_modules/linked": { hasInstallScript: true, link: true },
      "node_modules/plain": {},
      "tools/local": { hasInstallScript: true },
    }
    expect(installScriptNames(packages)).toEqual(["@scope/a", "b", "real", "tools/local"])
  })

  // npm 12 skips each install script that allowScripts does not name. A false entry skips it on npm 11 too.
  it("reviews each install script in the lock file", () => {
    const allowed = Object.keys(pkg.allowScripts ?? {})
    for (const name of installScriptNames(lock.packages)) {
      expect(
        allowed,
        `${name} has an install script. Add it to allowScripts of package.json.`,
      ).toContain(name)
    }
  })

  it("uses bare names in allowScripts", () => {
    for (const name of Object.keys(pkg.allowScripts ?? {})) {
      expect(name.indexOf("@", 1), name).toBe(-1)
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

  it("holds back new releases for 14 days", () => {
    expect(
      read(".npmrc")
        .split("\n")
        .map((l) => l.trim()),
    ).toContain("min-release-age=14")
  })
})
