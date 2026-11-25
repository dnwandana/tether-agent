<script setup lang="ts">
import { computed, nextTick, ref } from "vue"
import { useRouter } from "vue-router"
import { ChevronDownIcon, EllipsisIcon } from "@lucide/vue"
import { useCommand } from "../../lib/command"
import { guardOutside } from "@/lib/layer"
import { cn } from "@/lib/utils"
import { session } from "../../stores/session"
import { toast } from "../../stores/ui"
import { renderSystemText } from "../../lib/markdown"
import { buttonLabel, splitGlyph } from "../../lib/glyphs"
import ButtonLabel from "../ButtonLabel.vue"
import GlyphText from "../GlyphText.vue"
import { Button } from "@/components/ui/button"
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuGroup,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu"
import { Popover, PopoverAnchor, PopoverContent, PopoverTrigger } from "@/components/ui/popover"
import { Progress } from "@/components/ui/progress"
import {
  Dialog,
  DialogContent,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog"
import { Input } from "@/components/ui/input"
import { Textarea } from "@/components/ui/textarea"

const emit = defineEmits<{ "toggle-details": [] }>()
const router = useRouter()

const editing = ref(false)
const draftTitle = ref("")
const titleInput = ref<{ $el: HTMLInputElement } | null>(null)
// The chip that asked for the open reply. It is null for the reply of a menu action.
const pick = ref<"model" | "effort" | null>(null)
const promptOpen = ref(false)
const promptText = ref("")

// An action runs as a typed command. The reply menu and the confirm dialog come from useCommand.
const { run, reply, closeReply } = useCommand()

const modelLabel = computed(() =>
  session.entry ? `${session.model} (${session.entry})` : session.model,
)
const ctxText = computed(() =>
  session.context
    ? `~${(session.context.tokens / 1000).toFixed(1)}k / ${Math.round(session.context.window / 1000)}k`
    : "",
)
const ctxPercent = computed(() =>
  session.context ? Math.min(100, (session.context.tokens / session.context.window) * 100) : 0,
)
const ctxWarn = computed(
  () =>
    !!session.context &&
    session.context.tokens / session.context.window > session.context.threshold,
)

function closePop(): void {
  pick.value = null
  closeReply()
}

// A chip asks for its list when it opens. The menu shows when the reply arrives.
function onChip(kind: "model" | "effort", open: boolean): void {
  if (!open) {
    closePop()
    return
  }
  pick.value = kind
  closeReply()
  void run("/" + kind)
}

// A menu action runs a command. Its reply opens the plain reply popover, not a chip menu.
function act(text: string): void {
  pick.value = null
  void run(text)
}

// Returns the HTML of a Go reply. Its marks show as Lucide icons.
function html(src: string): string {
  return renderSystemText(src)
}

// The goal line with no lead mark, for the title of the goal button.
const goalTitle = computed(() => splitGlyph(session.goalLine).rest)

function startRename(): void {
  draftTitle.value = session.title
  editing.value = true
  void nextTick(() => titleInput.value?.$el?.focus())
}
function commitRename(): void {
  if (!editing.value) return
  editing.value = false
  const t = draftTitle.value.trim()
  if (t !== "" && t !== session.title) act("/rename " + t)
}
function cancelRename(): void {
  editing.value = false
}

function openGoal(): void {
  void router.push({ name: "goal", params: { session: session.id } })
}

function openPrompt(): void {
  promptText.value = ""
  promptOpen.value = true
}
function savePrompt(): void {
  const t = promptText.value.trim()
  if (t === "") return
  promptOpen.value = false
  void run("/prompt set " + t)
}
function clearPrompt(): void {
  promptOpen.value = false
  void run("/prompt clear")
}

// Downloads a file from an API route. A plain link cannot send the session header.
async function download(path: string, name: string): Promise<void> {
  try {
    const r = await fetch(path, {
      headers: { "X-Tether-Session": localStorage.getItem("tether.session") ?? "" },
    })
    if (!r.ok) {
      toast(`The download failed. The status is ${r.status}.`, "error")
      return
    }
    const url = URL.createObjectURL(await r.blob())
    const a = document.createElement("a")
    a.href = url
    a.download = name
    document.body.appendChild(a)
    a.click()
    a.remove()
    // The browser needs the URL for a while after the click to start the download.
    setTimeout(() => URL.revokeObjectURL(url), 30_000)
  } catch (e) {
    toast(e instanceof Error ? e.message : String(e), "error")
  }
}
function exportSession(): void {
  void download(
    "/api/sessions/" + encodeURIComponent(session.id) + "/export",
    `session-${session.id}.jsonl`,
  )
}
</script>

<template>
  <div class="flex min-w-0 items-center gap-2 border-b px-4 py-2">
    <Input
      v-if="editing"
      ref="titleInput"
      v-model="draftTitle"
      class="h-8 max-w-64"
      aria-label="Session title"
      @keydown.enter.prevent="commitRename"
      @keydown.esc.prevent="cancelRename"
      @blur="commitRename"
    />
    <span v-else-if="session.id === ''" class="truncate text-sm text-muted-foreground">
      New session
    </span>
    <Button
      v-else
      variant="ghost"
      size="sm"
      :class="
        cn('min-w-0 max-w-[40%] shrink justify-start', !session.title && 'text-muted-foreground')
      "
      title="Rename the session"
      @click="startRename"
    >
      <span class="truncate">{{ session.title || "New session" }}</span>
    </Button>

    <Popover
      :open="pick === null && reply !== null"
      @update:open="
        (o: boolean) => {
          if (!o) closePop()
        }
      "
    >
      <PopoverAnchor as-child>
        <div class="flex min-w-0 flex-1 flex-wrap items-center gap-2">
          <DropdownMenu
            :open="pick === 'model' && reply !== null"
            @update:open="(o: boolean) => onChip('model', o)"
          >
            <DropdownMenuTrigger as-child>
              <Button variant="outline" size="sm"
                >model {{ modelLabel }} <ChevronDownIcon
              /></Button>
            </DropdownMenuTrigger>
            <DropdownMenuContent
              align="start"
              :collision-padding="8"
              class="max-w-[min(420px,calc(100vw-16px))]"
            >
              <template v-if="reply">
                <!-- eslint-disable-next-line vue/no-v-html -- renderSystemText output only -->
                <DropdownMenuLabel class="font-normal" v-html="html(reply.text)" />
                <template v-for="(row, i) in reply.buttons" :key="i">
                  <DropdownMenuSeparator v-if="i > 0" />
                  <DropdownMenuGroup>
                    <DropdownMenuItem
                      v-for="(b, j) in row"
                      :key="j"
                      :disabled="b.data === '·'"
                      @select="run(b.data)"
                    >
                      <ButtonLabel :text="b.text" />
                      <span v-if="buttonLabel(b.text).current" class="sr-only">current</span>
                    </DropdownMenuItem>
                  </DropdownMenuGroup>
                </template>
              </template>
            </DropdownMenuContent>
          </DropdownMenu>

          <DropdownMenu
            :open="pick === 'effort' && reply !== null"
            @update:open="(o: boolean) => onChip('effort', o)"
          >
            <DropdownMenuTrigger as-child>
              <Button variant="outline" size="sm">
                effort {{ session.effort || "default" }} <ChevronDownIcon />
              </Button>
            </DropdownMenuTrigger>
            <DropdownMenuContent
              align="start"
              :collision-padding="8"
              class="max-w-[min(420px,calc(100vw-16px))]"
            >
              <template v-if="reply">
                <!-- eslint-disable-next-line vue/no-v-html -- renderSystemText output only -->
                <DropdownMenuLabel class="font-normal" v-html="html(reply.text)" />
                <template v-for="(row, i) in reply.buttons" :key="i">
                  <DropdownMenuSeparator v-if="i > 0" />
                  <DropdownMenuGroup>
                    <DropdownMenuItem
                      v-for="(b, j) in row"
                      :key="j"
                      :disabled="b.data === '·'"
                      @select="run(b.data)"
                    >
                      <ButtonLabel :text="b.text" />
                      <span v-if="buttonLabel(b.text).current" class="sr-only">current</span>
                    </DropdownMenuItem>
                  </DropdownMenuGroup>
                </template>
              </template>
            </DropdownMenuContent>
          </DropdownMenu>

          <Popover v-if="session.context">
            <PopoverTrigger as-child>
              <Button variant="ghost" size="sm" :class="cn(ctxWarn && 'text-warning')">
                {{ ctxText }}
                <span class="relative h-1 w-10 overflow-hidden rounded-full bg-muted">
                  <span
                    class="absolute inset-y-0 left-0 bg-primary"
                    :class="cn(ctxWarn && 'bg-warning')"
                    :style="{ width: ctxPercent + '%' }"
                  />
                </span>
              </Button>
            </PopoverTrigger>
            <PopoverContent
              align="start"
              class="flex w-64 flex-col gap-3"
              @pointer-down-outside="guardOutside"
              @interact-outside="guardOutside"
            >
              <div class="flex items-center justify-between text-sm">
                <span>Context</span>
                <span class="text-muted-foreground">{{ ctxText }}</span>
              </div>
              <div class="relative">
                <Progress :model-value="ctxPercent" aria-label="Context used" />
                <span
                  data-slot="context-threshold"
                  aria-hidden="true"
                  class="absolute inset-y-[-3px] w-px bg-warning"
                  :style="{ left: session.context.threshold * 100 + '%' }"
                />
              </div>
              <Button variant="secondary" size="sm" @click="act('/compact')">Compact</Button>
            </PopoverContent>
          </Popover>

          <Button
            v-if="session.goalLine !== ''"
            variant="secondary"
            size="sm"
            class="max-w-full min-w-[min(7rem,100%)]"
            :title="goalTitle"
            @click="openGoal"
          >
            <GlyphText :text="session.goalLine" truncate />
          </Button>
        </div>
      </PopoverAnchor>
      <PopoverContent
        align="start"
        :collision-padding="8"
        class="flex w-auto max-w-[min(420px,calc(100vw-16px))] flex-col gap-2"
        @pointer-down-outside="guardOutside"
        @interact-outside="guardOutside"
      >
        <template v-if="reply">
          <!-- eslint-disable-next-line vue/no-v-html -- renderSystemText output only -->
          <div class="text-sm" v-html="html(reply.text)" />
          <div v-for="(row, i) in reply.buttons" :key="i" class="flex flex-wrap gap-1.5">
            <Button
              v-for="(b, j) in row"
              :key="j"
              variant="secondary"
              size="sm"
              :disabled="b.data === '·'"
              :aria-pressed="buttonLabel(b.text).current || undefined"
              @click="run(b.data)"
            >
              <ButtonLabel :text="b.text" />
            </Button>
          </div>
        </template>
      </PopoverContent>
    </Popover>

    <div class="ml-auto flex shrink-0 items-center gap-1">
      <a
        class="px-2 text-sm text-muted-foreground hover:text-foreground"
        href="https://github.com/dnwandana/tether-agent/wiki/Dashboard#pages"
        target="_blank"
        rel="noreferrer"
        title="docs/wiki/Dashboard.md#pages"
        aria-label="Help for the pages: docs/wiki/Dashboard.md#pages"
        >?</a
      >
      <DropdownMenu>
        <DropdownMenuTrigger as-child>
          <Button variant="ghost" size="icon" aria-label="Session menu">
            <EllipsisIcon />
          </Button>
        </DropdownMenuTrigger>
        <DropdownMenuContent align="end">
          <DropdownMenuGroup>
            <template v-if="session.id !== ''">
              <DropdownMenuItem class="max-tab:min-h-11" @select="act('/fork')">
                Fork
              </DropdownMenuItem>
              <DropdownMenuItem class="max-tab:min-h-11" @select="act('/archive')">
                Archive
              </DropdownMenuItem>
              <DropdownMenuItem class="max-tab:min-h-11" @select="exportSession">
                Export JSONL
              </DropdownMenuItem>
            </template>
            <DropdownMenuItem class="max-tab:min-h-11" @select="openPrompt">
              Custom prompt…
            </DropdownMenuItem>
            <DropdownMenuItem
              v-if="session.context"
              class="tab:hidden max-tab:min-h-11"
              @select="act('/compact')"
            >
              Compact
            </DropdownMenuItem>
          </DropdownMenuGroup>
          <template v-if="session.id !== ''">
            <DropdownMenuSeparator />
            <DropdownMenuGroup>
              <DropdownMenuItem
                variant="destructive"
                class="max-tab:min-h-11"
                @select="act('/delete ' + session.id)"
              >
                Delete…
              </DropdownMenuItem>
            </DropdownMenuGroup>
          </template>
        </DropdownMenuContent>
      </DropdownMenu>
      <Button variant="ghost" size="sm" class="max-tab:hidden" @click="emit('toggle-details')">
        Details
      </Button>
    </div>

    <Dialog :open="promptOpen" @update:open="promptOpen = $event">
      <DialogContent
        :aria-describedby="undefined"
        @pointer-down-outside="guardOutside"
        @interact-outside="guardOutside"
      >
        <DialogHeader>
          <DialogTitle>Custom prompt of this session</DialogTitle>
        </DialogHeader>
        <Textarea v-model="promptText" rows="8" aria-label="Custom prompt" />
        <DialogFooter>
          <Button variant="secondary" @click="clearPrompt">Use the base prompt</Button>
          <Button @click="savePrompt">Save</Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  </div>
</template>
