// Dates and durations in one format for each page. The en-US locale gives the same text in
// each locale. "h23" gives "00:05" at midnight, where hour12: false can give "24:05".
const SHORT = new Intl.DateTimeFormat("en-US", {
  month: "short",
  day: "numeric",
  hour: "2-digit",
  minute: "2-digit",
  hourCycle: "h23",
})
const FULL = new Intl.DateTimeFormat("en-US", {
  weekday: "short",
  year: "numeric",
  month: "short",
  day: "numeric",
  hour: "2-digit",
  minute: "2-digit",
  second: "2-digit",
  hourCycle: "h23",
  timeZoneName: "short",
})
const MONTHS = ["Jan", "Feb", "Mar", "Apr", "May", "Jun", "Jul", "Aug", "Sep", "Oct", "Nov", "Dec"]

// Returns null for an empty value, an invalid date, and a time before 2000 (the Go zero time).
function toDate(t: string | number | Date | null | undefined): Date | null {
  if (t === null || t === undefined || t === "") return null
  const d = t instanceof Date ? t : new Date(t)
  if (Number.isNaN(d.getTime()) || d.getFullYear() < 2000) return null
  return d
}

// Returns "Nov 24 07:29", or "Jan 3, 2025 09:00" for another year.
export function shortTime(t: string | number | Date | null | undefined): string {
  const d = toDate(t)
  if (!d) return ""
  const p: Record<string, string> = {}
  for (const part of SHORT.formatToParts(d)) p[part.type] = part.value
  const year = d.getFullYear() === new Date().getFullYear() ? "" : `, ${d.getFullYear()}`
  return `${p.month} ${p.day}${year} ${p.hour}:${p.minute}`
}

// Returns the full local date and time with the zone, for a tooltip.
export function fullTime(t: string | number | Date | null | undefined): string {
  const d = toDate(t)
  return d ? FULL.format(d) : ""
}

// Returns "Nov 4" for the day key "2026-11-04". It does not parse the key as a UTC date.
export function dayLabel(key: string): string {
  const m = /^(\d{4})-(\d{2})-(\d{2})$/.exec(key)
  const month = m ? MONTHS[Number(m[2]) - 1] : undefined
  return m && month ? `${month} ${Number(m[3])}` : key
}

// Returns the time of a turn as "38 s" or "2 m 5 s".
export function duration(s: number): string {
  const total = Math.round(s)
  if (total < 60) return `${total} s`
  return `${Math.floor(total / 60)} m ${total % 60} s`
}
