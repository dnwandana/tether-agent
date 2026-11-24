import { render, screen, within } from "@testing-library/vue"
import userEvent from "@testing-library/user-event"
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
// The body rows, without the header row.
const bodyRows = () => within(screen.getAllByRole("rowgroup")[1]).getAllByRole("row")
const cellsOf = (row: HTMLElement) => within(row).getAllByRole("cell")
const text = (el: HTMLElement) => el.textContent?.trim() ?? ""

afterEach(() => vi.unstubAllGlobals())

describe("DataTable", () => {
  it("renders the headers, the cells, and the card labels", () => {
    render(DataTable, { props: { columns, rows, rowKey: "id" } })
    expect(screen.getAllByRole("columnheader").map(text)).toEqual(["Job", "Next run", "Cost", ""])
    const cells = cellsOf(bodyRows()[0])
    expect(cells.map(text)).toEqual(["nightly-backup", "02:00", "$0.0120", ""])
    expect(cells[0].hasAttribute("data-first")).toBe(true)
    expect(cells[0].getAttribute("data-l")).toBe("")
    expect(cells[1].getAttribute("data-l")).toBe("Next run")
    expect(cells[2].className).toContain("text-right")
    expect(screen.getAllByRole("columnheader")[2].className).toContain("text-right")
    expect(cells[3].hasAttribute("data-act-col")).toBe(true)
    expect(cells[3].getAttribute("data-l")).toBe("")
    expect(screen.getByRole("table").closest("[data-layout]")?.getAttribute("data-layout")).toBe(
      "table",
    )
  })

  it("uses the cell slot", () => {
    render(DataTable, {
      props: { columns, rows, rowKey: "id" },
      slots: {
        "cell-name":
          '<template #cell-name="{ row, value }"><b>{{ value }} ({{ row.id }})</b></template>',
      },
    })
    expect(screen.getByText("nightly-backup (j1)").tagName).toBe("B")
  })

  it("prints nothing when a cell slot renders only comments", () => {
    render(DataTable, {
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
    expect(cellsOf(bodyRows()[0]).map(text)).toEqual(["deploy", "", ""])
  })

  it("prints only a string or a number when the column has no slot", () => {
    render(DataTable, {
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
    expect(cellsOf(bodyRows()[0]).map(text)).toEqual(["text", "0", "", "", "", ""])
  })

  it("emits row-click with the row", async () => {
    const user = userEvent.setup()
    const { emitted } = render(DataTable, { props: { columns, rows, rowKey: "id" } })
    await user.click(screen.getByText("gh-digest"))
    expect(emitted()["row-click"]).toEqual([[rows[1]]])
  })

  it("shows Nothing to show. with no rows", () => {
    render(DataTable, { props: { columns, rows: [], rowKey: "id" } })
    const cell = screen.getByText("Nothing to show.")
    expect(cell.getAttribute("colspan")).toBe("4")
  })

  it("becomes a card list below cardsBelow", () => {
    const asked: string[] = []
    vi.stubGlobal("matchMedia", (q: string) => {
      asked.push(q)
      return { matches: true, media: q, addEventListener: () => {}, removeEventListener: () => {} }
    })
    render(DataTable, { props: { columns, rows, rowKey: "id", cardsBelow: 1024 } })
    expect(asked).toContain("(max-width: 1023px)")
    expect(screen.getByRole("table").closest("[data-layout]")?.getAttribute("data-layout")).toBe(
      "cards",
    )
    const cell = cellsOf(bodyRows()[0])[1]
    expect(cell.className).toContain("before:content-[attr(data-l)]")
  })
})
