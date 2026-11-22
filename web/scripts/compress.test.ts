import { mkdirSync, mkdtempSync, readFileSync, writeFileSync, existsSync } from "node:fs"
import { tmpdir } from "node:os"
import { join } from "node:path"
import { brotliDecompressSync, gunzipSync } from "node:zlib"
import { describe, expect, it } from "vitest"
import { compressDir } from "./compress.mjs"

function fixture() {
  const dir = mkdtempSync(join(tmpdir(), "tether-dist-"))
  mkdirSync(join(dir, "assets"))
  writeFileSync(join(dir, "index.html"), "<!doctype html><title>Tether</title>".repeat(40))
  writeFileSync(join(dir, "assets", "index-a1b2c3.js"), "export const x = 1;\n".repeat(200))
  writeFileSync(join(dir, ".source-hash"), "abc123\n")
  writeFileSync(join(dir, "assets", "mark.png"), Buffer.from([0x89, 0x50, 0x4e, 0x47]))
  return dir
}

describe("compressDir", () => {
  it("writes a .gz and a .br copy of each text file", () => {
    const dir = fixture()
    expect(compressDir(dir)).toBe(4)
    for (const name of ["index.html", "assets/index-a1b2c3.js"]) {
      const raw = readFileSync(join(dir, name))
      expect(gunzipSync(readFileSync(join(dir, name + ".gz")))).toEqual(raw)
      expect(brotliDecompressSync(readFileSync(join(dir, name + ".br")))).toEqual(raw)
    }
  })

  it("skips dot files, binary files, and old copies", () => {
    const dir = fixture()
    compressDir(dir)
    expect(existsSync(join(dir, ".source-hash.gz"))).toBe(false)
    expect(existsSync(join(dir, "assets", "mark.png.gz"))).toBe(false)
    expect(compressDir(dir)).toBe(4)
    expect(existsSync(join(dir, "index.html.gz.gz"))).toBe(false)
    expect(existsSync(join(dir, "index.html.br.br"))).toBe(false)
  })

  it("gives the same bytes on each run", () => {
    const a = fixture()
    const b = fixture()
    compressDir(a)
    compressDir(b)
    expect(readFileSync(join(a, "index.html.gz"))).toEqual(readFileSync(join(b, "index.html.gz")))
    expect(readFileSync(join(a, "index.html.br"))).toEqual(readFileSync(join(b, "index.html.br")))
  })
})
