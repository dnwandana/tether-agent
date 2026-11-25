// The width rule of a column. DataTable gives each type a width in the fixed table layout.
export type ColumnType = "text" | "wrap" | "block" | "num" | "date" | "badge" | "act"

// One column of a DataTable. width is a CSS length in rem, for example "11rem".
export interface Column {
  key: string
  label: string
  width?: string
  align?: "left" | "right"
  type?: ColumnType
  optional?: boolean
}

// Returns the width rule of a column. The actions key is always act.
export function columnType(c: Column): ColumnType {
  if (c.key === "actions") return "act"
  if (c.type) return c.type
  return c.align === "right" ? "num" : "text"
}

const TYPE_REM: Record<ColumnType, number> = {
  text: 10,
  wrap: 10,
  block: 10,
  num: 6,
  date: 9,
  badge: 8,
  act: 0,
}

// Returns the minimum width of a table in rem: the fixed widths plus 10 rem for each text, wrap,
// and block column. withOptional false leaves out the optional columns.
export function tableMinRem(cols: Column[], withOptional: boolean): number {
  let sum = 0
  for (const c of cols) {
    if (c.optional && !withOptional) continue
    const w = c.width ? parseFloat(c.width) : NaN
    sum += Number.isNaN(w) ? TYPE_REM[columnType(c)] : w
  }
  return sum
}
