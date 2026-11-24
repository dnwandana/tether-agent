// Keeps the bucket and scale rules of the Usage chart. All key arithmetic is in UTC,
// so a DST change in the browser zone cannot give a duplicate key.

export type Grain = "day" | "week" | "month"

const DAY_MS = 86_400_000

// Returns a number with two digits.
function pad2(n: number): string {
  return String(n).padStart(2, "0")
}

// Returns the UTC time of a "YYYY-MM-DD" key.
function utc(key: string): number {
  const [y, m, d] = key.split("-").map(Number)
  return Date.UTC(y, m - 1, d)
}

// Returns the "YYYY-MM-DD" key of a UTC time.
function keyOf(t: number): string {
  const d = new Date(t)
  return `${d.getUTCFullYear()}-${pad2(d.getUTCMonth() + 1)}-${pad2(d.getUTCDate())}`
}

// Returns the bucket grain of a range: only "all" groups, by week or by month.
export function grainFor(range: string, from: string, to: string): Grain {
  if (range !== "all" || from === "" || to === "") return "day"
  const span = (utc(to) - utc(from)) / DAY_MS
  if (span > 365) return "month"
  if (span > 60) return "week"
  return "day"
}

// Returns the bucket key of a day key: the day, the Monday of its week, or its month.
export function bucketOf(dayKey: string, grain: Grain): string {
  if (grain === "day") return dayKey
  if (grain === "month") return dayKey.slice(0, 7)
  const t = utc(dayKey)
  const back = (new Date(t).getUTCDay() + 6) % 7
  return keyOf(t - back * DAY_MS)
}

// Returns each bucket key from the bucket of the first day to the bucket of the last day.
export function bucketKeys(from: string, to: string, grain: Grain): string[] {
  if (from === "" || to === "") return []
  const first = bucketOf(from, grain)
  const last = bucketOf(to, grain)
  const out: string[] = []
  if (grain === "month") {
    let [y, m] = first.split("-").map(Number)
    for (;;) {
      const key = `${y}-${pad2(m)}`
      out.push(key)
      if (key >= last) break
      m += 1
      if (m > 12) {
        m = 1
        y += 1
      }
    }
    return out
  }
  const step = grain === "week" ? 7 : 1
  for (let t = utc(first); ; t += step * DAY_MS) {
    const key = keyOf(t)
    out.push(key)
    if (key >= last) break
  }
  return out
}

// Returns the next value of the form 1, 2, or 5 times a power of ten at or above max.
export function niceMax(max: number): number {
  if (!(max > 0)) return 1
  const e = Math.floor(Math.log10(max))
  for (const m of [1, 2, 5, 10]) {
    const v = Number((m * 10 ** e).toPrecision(12))
    if (v >= max) return v
  }
  return 10 ** (e + 1)
}

// Returns the dollar text of a scale tick with the fewest decimals that keep the value.
export function formatTick(v: number): string {
  if (v === 0) return "$0"
  if (Number.isInteger(v)) return "$" + v
  for (let d = 2; d <= 8; d++) {
    if (Math.abs(Number(v.toFixed(d)) - v) < 1e-12) return "$" + v.toFixed(d)
  }
  return "$" + v.toFixed(8)
}

// Returns the step between two date labels, with about 56 px for each label.
export function labelStep(buckets: number, width: number): number {
  return Math.max(1, Math.ceil((buckets * 56) / width))
}
