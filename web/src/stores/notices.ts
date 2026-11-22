import { reactive } from "vue"
import type { Notice, Snapshot } from "../api/types.gen"

type Batch = { name: string; data: unknown }[]

// NOTICE_MAX is the size of the notice buffer of the server.
const NOTICE_MAX = 20

// notices holds the messages that came with no live turn: the boot notice, goal notices,
// cron deliveries, and the replies of resumed turns. The newest is last.
export const notices = reactive({ list: [] as Notice[] })

export function applyNoticeEvents(batch: Batch): void {
  for (const e of batch) {
    switch (e.name) {
      case "hello":
        notices.list = [...((e.data as Snapshot).notices ?? [])]
        break
      case "notice": {
        const n = e.data as Notice
        notices.list = [...notices.list, n].slice(-NOTICE_MAX)
        break
      }
    }
  }
}
