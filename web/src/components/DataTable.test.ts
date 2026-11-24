import { mount } from "@vue/test-utils"
import { afterEach, describe, expect, it, vi } from "vitest"
import DataTable from "./DataTable.vue"

const columns = [
  { key: "name", label: "Job" },
  { key: "next", label: "Next run" },
  { key: "cost", label: "Cost", align: "right" as const },
  { key: "actions", label: "" },
]
const rows = [
  { id: "j1", name: "nightly-backup", next: "02:00", cost: "$0.0120" },
  { id: "j2", name: "gh-digest", next: "07:00", cost: "$0.0031" },
]

afterEach(() => vi.unstubAllGlobals())

describe("DataTable", () => {
  it("renders the headers, the cells, and the card labels", () => {
    const w = mount(DataTable, { props: { columns, rows, rowKey: "id" } })
    expect(w.findAll("th").map((t) => t.text())).toEqual(["Job", "Next run", "Cost", ""])
    const cells = w.findAll("tbody tr")[0].findAll("td")
    expect(cells.map((c) => c.text())).toEqual(["nightly-backup", "02:00", "$0.0120", ""])
    expect(cells[0].classes()).toContain("first")
    expect(cells[0].attributes("data-l")).toBe("")
    expect(cells[1].attributes("data-l")).toBe("Next run")
    expect(cells[2].classes()).toContain("num")
    expect(cells[3].classes()).toContain("act")
    expect(w.find("table").classes()).not.toContain("rt-cards")
  })

  it("uses the cell slot", () => {
    const w = mount(DataTable, {
      props: { columns, rows, rowKey: "id" },
      slots: {
        "cell-name":
          '<template #cell-name="{ row, value }"><b class="nm">{{ value }} ({{ row.id }})</b></template>',
      },
    })
    expect(w.find(".nm").text()).toBe("nightly-backup (j1)")
  })

  it("prints nothing when a cell slot renders only comments", () => {
    const w = mount(DataTable, {
      props: {
        columns: [
          { key: "name", label: "Skill" },
          { key: "tags", label: "Tags" },
          { key: "n", label: "Uses", align: "right" as const },
        ],
        rows: [{ id: "k1", name: "deploy", tags: [], n: 7 }],
        rowKey: "id",
      },
      slots: {
        "cell-tags": '<template #cell-tags><button v-if="false">Add</button></template>',
        "cell-n": '<template #cell-n><button v-if="false">Reset</button></template>',
      },
    })
    const cells = w.findAll("tbody tr")[0].findAll("td")
    expect(cells.map((c) => c.text())).toEqual(["deploy", "", ""])
  })

  it("prints only a string or a number when the column has no slot", () => {
    const w = mount(DataTable, {
      props: {
        columns: [
          { key: "s", label: "S" },
          { key: "zero", label: "Zero" },
          { key: "list", label: "List" },
          { key: "obj", label: "Obj" },
          { key: "none", label: "None" },
          { key: "missing", label: "Missing" },
        ],
        rows: [{ id: "r1", s: "text", zero: 0, list: ["a", "b"], obj: { a: 1 }, none: null }],
        rowKey: "id",
      },
    })
    const cells = w.findAll("tbody tr")[0].findAll("td")
    expect(cells.map((c) => c.text())).toEqual(["text", "0", "", "", "", ""])
  })

  it("emits row-click with the row", async () => {
    const w = mount(DataTable, { props: { columns, rows, rowKey: "id" } })
    await w.findAll("tbody tr")[1].trigger("click")
    expect(w.emitted("row-click")).toEqual([[rows[1]]])
  })

  it("becomes a card list below cardsBelow", () => {
    const asked: string[] = []
    vi.stubGlobal("matchMedia", (q: string) => {
      asked.push(q)
      return { matches: true, media: q, addEventListener: () => {}, removeEventListener: () => {} }
    })
    const w = mount(DataTable, { props: { columns, rows, rowKey: "id", cardsBelow: 1024 } })
    expect(asked).toContain("(max-width: 1023px)")
    expect(w.find("table").classes()).toContain("rt-cards")
  })
})
