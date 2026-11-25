const KEY_PREFIX = "tether.rows."

// Lists the choices of the "Rows per page" select.
export const PAGE_SIZES = [10, 20, 50, 100] as const

// Names one choice of the "Rows per page" select.
export type PageSize = (typeof PAGE_SIZES)[number]

// Holds the rows per page when no choice is stored.
export const DEFAULT_SIZE: PageSize = 20

// Returns the stored rows per page of a table, or 20.
export function readSize(pageKey: string): PageSize {
  try {
    const stored = globalThis.localStorage.getItem(KEY_PREFIX + pageKey)
    return PAGE_SIZES.find((n) => String(n) === stored) ?? DEFAULT_SIZE
  } catch {
    return DEFAULT_SIZE
  }
}

// Stores the rows per page of a table.
export function writeSize(pageKey: string, n: PageSize): void {
  try {
    globalThis.localStorage.setItem(KEY_PREFIX + pageKey, String(n))
  } catch {
    // Storage can be blocked. The choice then lasts for this page only.
  }
}

// Returns the number of the last page, never less than 1.
export function lastPage(total: number, size: number): number {
  return Math.max(1, Math.ceil(total / size))
}

// Returns the page moved into the range from 1 to the last page.
export function clampPage(page: number, total: number, size: number): number {
  return Math.min(Math.max(1, page), lastPage(total, size))
}

// Returns the page number of a URL value, or 1 when the value is not valid.
export function parsePage(v: unknown): number {
  if (typeof v === "string" && /^\d+$/.test(v) && Number(v) >= 1) return Number(v)
  return 1
}

// Returns true when the table has more rows than the smallest page size.
export function pagerVisible(total: number): boolean {
  return total > 10
}

// Returns the first and the last row number of a page, or 0 and 0 for no rows.
export function pageRange(
  page: number,
  size: number,
  total: number,
): { start: number; end: number } {
  if (total <= 0) return { start: 0, end: 0 }
  return { start: (page - 1) * size + 1, end: Math.min(page * size, total) }
}
