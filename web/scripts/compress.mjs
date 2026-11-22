import { readdirSync, readFileSync, writeFileSync } from "node:fs"
import { extname, join } from "node:path"
import { pathToFileURL } from "node:url"
import { brotliCompressSync, constants, gzipSync } from "node:zlib"

const TEXT_EXTENSIONS = new Set([".html", ".js", ".css", ".svg", ".json"])

/**
 * Writes a .gz and a .br copy next to each text file under dir, and returns the number of
 * copies that it wrote.
 * @param {string} dir
 * @returns {number}
 */
export function compressDir(dir) {
  let count = 0
  for (const entry of readdirSync(dir, { withFileTypes: true })) {
    const name = entry.name
    if (name.startsWith(".") || name.endsWith(".gz") || name.endsWith(".br")) continue
    const path = join(dir, name)
    if (entry.isDirectory()) {
      count += compressDir(path)
      continue
    }
    if (!entry.isFile() || !TEXT_EXTENSIONS.has(extname(name))) continue
    const data = readFileSync(path)
    writeFileSync(path + ".gz", gzipSync(data, { level: 9 }))
    writeFileSync(
      path + ".br",
      brotliCompressSync(data, {
        params: {
          [constants.BROTLI_PARAM_QUALITY]: 11,
          [constants.BROTLI_PARAM_SIZE_HINT]: data.length,
        },
      }),
    )
    count += 2
  }
  return count
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  console.log(`compress: wrote ${compressDir(process.argv[2])} files`)
}
