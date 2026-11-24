import { shortTime } from "./time"

const DAYS = ["Sun", "Mon", "Tue", "Wed", "Thu", "Fri", "Sat"]

function pad(n: number): string {
  return String(n).padStart(2, "0")
}

// Returns the schedule text in the browser zone. A cron expression runs in the host zone, so the fixed hour moves by the difference of the two offsets.
export function readableSchedule(
  raw: string,
  hostZone: string,
  hostOffsetMin: number,
  browserOffsetMin: number,
): { text: string; title: string } {
  const title = hostZone ? `${raw} · host time ${hostZone}` : raw
  const fallback = { text: `${raw} (host time)`, title }

  const every = /^every (\d+)([mhd])$/.exec(raw)
  if (every) {
    const unit = { m: "min", h: "h", d: "d" }[every[2] as "m" | "h" | "d"]
    return { text: `every ${every[1]} ${unit}`, title }
  }

  if (/^\d{4}-\d{2}-\d{2}/.test(raw)) {
    const t = shortTime(raw)
    return t ? { text: `once ${t}`, title } : fallback
  }

  // With no host zone, the host offset is not known, so a shift of the hour can be wrong.
  if (!hostZone) return fallback
  const f = raw.split(" ")
  if (f.length !== 5 || f[2] !== "*" || f[3] !== "*") return fallback
  if (!/^\d{1,2}$/.test(f[0]) || !/^\d{1,2}$/.test(f[1])) return fallback
  const minute = Number(f[0])
  const hour = Number(f[1])
  if (minute > 59 || hour > 23) return fallback

  const total = hour * 60 + minute + (browserOffsetMin - hostOffsetMin)
  const shift = Math.floor(total / 1440)
  const mins = ((total % 1440) + 1440) % 1440
  const at = `${pad(Math.floor(mins / 60))}:${pad(mins % 60)}`

  if (f[4] === "*") return { text: `daily ${at}`, title }
  if (f[4] === "1-5") {
    if (shift === 0) return { text: `weekdays ${at}`, title }
    const first = DAYS[(1 + shift + 7) % 7]
    const last = DAYS[(5 + shift + 7) % 7]
    return { text: `${first}–${last} ${at}`, title }
  }
  if (/^[0-7]$/.test(f[4])) {
    const day = DAYS[(Number(f[4]) + shift + 7) % 7]
    return { text: `weekly ${day} ${at}`, title }
  }
  return fallback
}
