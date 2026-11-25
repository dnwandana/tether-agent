import { describe, it, expect } from "vitest"

// Reads the source of each component and page as text.
const sources = import.meta.glob<string>("../**/*.vue", {
  query: "?raw",
  import: "default",
  eager: true,
})

// Returns each <DataTable …> start tag of a file. A ">" in a quoted attribute value does not
// end the tag, so an arrow function in a handler is safe.
function dataTableTags(src: string): string[] {
  const out: string[] = []
  for (const m of src.matchAll(/<DataTable(?=[\s/>])/g)) {
    let quote = false
    let end = m.index
    while (end < src.length && (quote || src[end] !== ">")) {
      if (src[end] === '"') quote = !quote
      end++
    }
    out.push(src.slice(m.index, end + 1))
  }
  return out
}

// Tells whether a tag has the attribute, static or bound.
const has = (tag: string, name: string) => new RegExp(`\\s(:|v-bind:)?${name}=`).test(tag)

const tags = Object.entries(sources).flatMap(([file, src]) =>
  dataTableTags(src).map((tag) => ({ file, tag })),
)

describe("DataTable usage", () => {
  it("finds the DataTable tags of the pages", () => {
    expect(new Set(tags.map((t) => t.file)).size).toBeGreaterThanOrEqual(18)
  })

  it("gives each DataTable a page-key and a page-label", () => {
    const bad = tags
      .filter((t) => !has(t.tag, "page-key") || !has(t.tag, "page-label"))
      .map((t) => `${t.file}: ${t.tag.split("\n")[0]}`)
    expect(bad).toEqual([])
  })

  it("gives each DataTable its own page-key", () => {
    const keys = tags.map((t) => /\spage-key="([^"]+)"/.exec(t.tag)?.[1]).filter(Boolean)
    expect(keys.filter((k, i) => keys.indexOf(k) !== i)).toEqual([])
  })
})
