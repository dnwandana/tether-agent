import { ref, watch, type Ref } from "vue"
import { useRoute, useRouter } from "vue-router"
import { parsePage } from "@/lib/pager"
import { rememberListQuery } from "@/stores/ui"

// Keeps the page of the main table of a list page in the route query as "page".
export function useRoutePage(routeName: string): {
  page: Ref<number>
  setPage: (p: number) => void
} {
  const route = useRoute()
  const router = useRouter()
  const page = ref(parsePage(route.query.page))

  watch(
    () => route.query.page,
    (v) => {
      if (route.name === routeName) page.value = parsePage(v)
    },
  )

  function setPage(p: number): void {
    page.value = p
    if (route.name !== routeName) return
    const want = p > 1 ? String(p) : undefined
    if (route.query.page === want) return
    const query = { ...route.query }
    if (want === undefined) delete query.page
    else query.page = want
    void router.replace({ query })
    rememberListQuery(routeName, query)
  }

  return { page, setPage }
}
