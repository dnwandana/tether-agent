import { defineComponent, h, type Component } from "vue"
import { createRouter, createWebHistory, type RouteMeta, type RouteRecordRaw } from "vue-router"
import { prefs } from "./stores/ui"

declare module "vue-router" {
  interface RouteMeta {
    ownScroll?: boolean
  }
}

type RouteComponent = Component

// The page modules that exist in the pages folder. A route with no page file shows an empty page.
const pages = import.meta.glob<{ default: RouteComponent }>("./pages/*.vue")

const emptyPage = defineComponent({ render: () => h("div", { class: "page" }) })

// Returns the lazy page of the file, or an empty page when the file does not exist yet.
function page(file: string): () => Promise<RouteComponent> {
  const load = pages["./pages/" + file]
  if (!load) return () => Promise.resolve(emptyPage)
  return async () => (await load()).default
}

// Returns true when the browser has a stored session header.
function hasSession(): boolean {
  try {
    return !!localStorage.getItem("tether.session")
  } catch {
    return false
  }
}

const table: [string, string, string, RouteMeta?][] = [
  ["chat", "/chat", "ChatPage.vue", { ownScroll: true }],
  ["sessions", "/sessions", "SessionsPage.vue"],
  ["transcript", "/sessions/:id", "TranscriptPage.vue"],
  ["jobs", "/jobs", "JobsPage.vue"],
  ["job", "/jobs/:id", "JobDetailPage.vue"],
  ["goals", "/goals", "GoalsPage.vue"],
  ["goal", "/goals/:session", "GoalDetailPage.vue"],
  ["security", "/security", "SecurityPage.vue"],
  ["skills", "/skills", "SkillsPage.vue"],
  ["skill", "/skills/:name", "SkillDetailPage.vue"],
  ["memory", "/memory", "MemoryPage.vue"],
  ["usage", "/usage", "UsagePage.vue"],
  ["models", "/models", "ModelsPage.vue"],
  ["system", "/system", "SystemPage.vue"],
  ["ops", "/system/ops", "OpsPage.vue"],
  ["access", "/access", "AccessPage.vue"],
  ["start", "/start", "GettingStartedPage.vue"],
  ["signin", "/signin", "SignInPage.vue"],
]

const routes: RouteRecordRaw[] = [
  {
    path: "/",
    redirect: (to) => {
      if (to.hash.startsWith("#code=")) return { name: "signin", hash: to.hash }
      const last = prefs.lastRoute
      if (last && last !== "/" && !last.startsWith("/signin")) return last
      return { name: "chat" }
    },
  },
  ...table.map(([name, path, file, meta]) => ({
    name,
    path,
    component: page(file),
    meta: meta ?? {},
  })),
  { path: "/:rest(.*)*", redirect: () => ({ name: "chat", params: {} }) },
]

// The scrollTop of the .scroll box for each history entry, by history.state.position.
const positions = new Map<number, number>()
const POSITIONS_MAX = 50
// The scrollTop to restore after a back or a forward. The page restores it after its first load.
let pendingTop: number | null = null
// The history position of the page on screen.
let leftPosition: number | null = null

// Returns the page scroll box, or null on Chat.
function scrollBox(): HTMLElement | null {
  return document.querySelector<HTMLElement>(".scroll")
}

// Returns "auto" when the system asks for reduced motion, else "smooth".
function motion(): ScrollBehavior {
  return globalThis.matchMedia?.("(prefers-reduced-motion: reduce)")?.matches ? "auto" : "smooth"
}

// Returns history.state.position, or null when the state has no position.
function historyPosition(): number | null {
  const p = (globalThis.history?.state as { position?: unknown } | null)?.position
  return typeof p === "number" ? p : null
}

export const router = createRouter({
  history: createWebHistory(),
  routes,
  // The router scrolls the .scroll box, not the window. It always returns false.
  // A hash like #card-<id> scrolls to the element with that id.
  scrollBehavior(to, _from, saved) {
    if (to.hash) {
      let el: HTMLElement | null = null
      try {
        el = document.getElementById(decodeURIComponent(to.hash.slice(1)))
      } catch {
        el = null
      }
      if (el) {
        el.scrollIntoView({ behavior: motion(), block: "start" })
        el.classList.add("pulse")
        el.addEventListener("animationend", () => el.classList.remove("pulse"), { once: true })
      }
    } else if (saved) {
      const pos = historyPosition()
      pendingTop = pos === null ? null : (positions.get(pos) ?? null)
    } else {
      const box = scrollBox()
      if (box) box.scrollTop = 0
      pendingTop = null
    }
    return false
  },
})

// Sets the scroll box to the position of a back or a forward, once. A page calls it after its
// first load ends, because a restore before the data renders is cut short.
export function restorePendingScroll(): void {
  const box = scrollBox()
  if (pendingTop !== null && box) box.scrollTop = pendingTop
  pendingTop = null
}

router.beforeEach((to) => {
  const box = scrollBox()
  // On a back, history.state already holds the target entry, so use the entry that we left.
  if (box && leftPosition !== null) {
    // Delete first, so that the Map keeps the newest entry last.
    positions.delete(leftPosition)
    positions.set(leftPosition, box.scrollTop)
    // Keep the newest entries only.
    while (positions.size > POSITIONS_MAX) positions.delete(positions.keys().next().value as number)
  }
  if (to.name !== "signin" && !hasSession()) return { name: "signin", hash: to.hash }
})

router.afterEach((to) => {
  leftPosition = historyPosition()
  if (to.name !== "signin") prefs.lastRoute = to.fullPath
})
