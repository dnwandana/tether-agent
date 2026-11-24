import { reactive } from "vue"

// The time zone of the host. A cron expression runs in this zone. The bootstrap route fills it.
export const host = reactive<{ zone: string; offsetMin: number }>({ zone: "", offsetMin: 0 })

// Keeps the zone name and the offset in minutes east of UTC.
export function setHost(zone: string, offsetMin: number): void {
  host.zone = zone
  host.offsetMin = offsetMin
}
