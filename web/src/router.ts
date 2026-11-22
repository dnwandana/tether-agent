import { defineComponent, h, type Component } from "vue"
import { createRouter, createWebHistory, type RouteRecordRaw } from "vue-router"
import { prefs } from "./stores/ui"

type RouteComponent = Component

// The page modules that exist now. A later task adds each page file.
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

const table: [string, string, string][] = [
  ["chat", "/chat", "ChatPage.vue"],
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
  ...table.map(([name, path, file]) => ({ name, path, component: page(file) })),
  { path: "/:rest(.*)*", redirect: () => ({ name: "chat", params: {} }) },
]

export const router = createRouter({
  history: createWebHistory(),
  routes,
  // A hash like #card-<id> scrolls to the element with that id.
  scrollBehavior(to, _from, saved) {
    if (to.hash) return { el: to.hash, behavior: "smooth" }
    return saved ?? false
  },
})

router.beforeEach((to) => {
  if (to.name !== "signin" && !hasSession()) return { name: "signin", hash: to.hash }
})

router.afterEach((to) => {
  if (to.name !== "signin") prefs.lastRoute = to.fullPath
})
