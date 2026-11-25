import { describe, expect, it } from "vitest"
import { columnType, tableMinRem, type Column } from "./table"

const wide: Column[] = [
  { key: "name", label: "Name" },
  { key: "when", label: "When", type: "date" },
  { key: "state", label: "State", type: "badge" },
  { key: "n", label: "Uses", align: "right" },
  { key: "note", label: "Note", type: "wrap" },
  { key: "actions", label: "", width: "11rem" },
]
const withOpt: Column[] = [
  { key: "name", label: "Name" },
  { key: "started", label: "Started", type: "date", optional: true },
  { key: "n", label: "Messages", align: "right", optional: true },
  { key: "actions", label: "", width: "6rem" },
]

describe("columnType", () => {
  it("gives act to the actions key, also with align or type", () => {
    expect(columnType({ key: "actions", label: "", align: "right" })).toBe("act")
    expect(columnType({ key: "actions", label: "", type: "text" })).toBe("act")
  })

  it("gives num to align right with no type", () => {
    expect(columnType({ key: "n", label: "N", align: "right" })).toBe("num")
  })

  it("keeps the type when the column sets one", () => {
    expect(columnType({ key: "n", label: "N", align: "right", type: "date" })).toBe("date")
    expect(columnType({ key: "x", label: "X", type: "block" })).toBe("block")
  })

  it("gives text by default", () => {
    expect(columnType({ key: "x", label: "X" })).toBe("text")
  })
})

describe("tableMinRem", () => {
  it("adds the type widths, 10 rem for each free column, and the act width", () => {
    expect(tableMinRem(wide, true)).toBe(54)
  })

  it("leaves out the optional columns on request", () => {
    expect(tableMinRem(withOpt, true)).toBe(31)
    expect(tableMinRem(withOpt, false)).toBe(16)
  })

  it("uses the column width over the type width", () => {
    expect(tableMinRem([{ key: "n", label: "N", align: "right", width: "7.5rem" }], true)).toBe(7.5)
  })

  it("counts an act column with no width as 0", () => {
    expect(tableMinRem([{ key: "actions", label: "" }], true)).toBe(0)
  })
})
