<script setup lang="ts">
import { computed, ref } from "vue"
import type { FileRef, MessageView, ToolView } from "../../api/types.gen"
import { session, loadMessages } from "../../stores/session"
import { turn } from "../../stores/turn"
import { renderMarkdown } from "../../lib/markdown"
import StreamText from "./StreamText.vue"
import ToolLine from "./ToolLine.vue"

defineProps<{ selected?: string }>()
const emit = defineEmits<{ tool: [view: ToolView] }>()

// The number of saved messages to draw, counted from the newest.
const drawn = ref(150)
// False when the last older load added no message.
const more = ref(true)

function shown(m: MessageView): boolean {
  return m.role === "summary" || m.role === "user" || m.role === "assistant"
}

const list = computed(() => session.messages.filter(shown).slice(-drawn.value))

// The server flag tells if older rows exist. A page with tool calls merges to fewer rows, so
// the count of loaded rows cannot tell it.
const canOlder = computed(
  () => more.value && (session.hasOlder || session.messages.filter(shown).length > drawn.value),
)

// Draws 100 more messages, and loads an older page when all loaded messages are drawn.
async function older(): Promise<void> {
  const reached = drawn.value >= session.messages.length
  drawn.value += 100
  if (!reached || session.messages.length === 0) return
  const before = session.messages.length
  // A summary row has a negative id. The server accepts only a positive id for before.
  const first = session.messages.find((m) => m.id > 0)
  if (!first) return
  try {
    await loadMessages({ before: first.id })
  } catch (e) {
    console.warn("older messages load failed", e)
    return
  }
  if (session.messages.length === before) more.value = false
}

function fileUrl(f: FileRef): string {
  return "/api/files/" + encodeURIComponent(f.id)
}

function html(src: string): string {
  return renderMarkdown(src)
}

const live = computed(() => turn.view)
</script>

<template>
  <div class="transcript">
    <button
      v-if="canOlder"
      type="button"
      class="btn btn-ghost btn-sm"
      data-act="older"
      @click="older"
    >
      Load older messages
    </button>
    <template v-for="m in list" :key="m.id">
      <div v-if="m.role === 'summary'" class="msg">
        <details class="collapsed">
          <summary>🗜 Compacted summary</summary>
          <span>{{ m.text }}</span>
        </details>
      </div>
      <div
        v-else-if="m.role === 'user'"
        class="msg msg-user"
        :class="{ 'is-compacted': m.compacted }"
      >
        <div class="bubble-user" style="white-space: pre-wrap">{{ m.text }}</div>
      </div>
      <div v-else class="msg msg-assistant" :class="{ 'is-compacted': m.compacted }">
        <div class="msg-meta">
          <span class="avatar avatar-agent" aria-hidden="true">T</span>
          <span>Tether</span>
          <span v-if="m.model">{{ m.model }}</span>
          <span v-if="m.answered_by" class="chip chip-warn">answered by {{ m.answered_by }}</span>
        </div>
        <slot name="reasoning" :message="m" />
        <div v-if="m.tools.length > 0" class="tools">
          <ToolLine
            v-for="t in m.tools"
            :key="t.call"
            :tool="t"
            :selected="selected === t.call"
            @select="emit('tool', t)"
          />
        </div>
        <!-- eslint-disable-next-line vue/no-v-html -- renderMarkdown output only -->
        <div v-if="m.text !== ''" class="answer" v-html="html(m.text)" />
        <div v-for="f in m.files" :key="f.id" class="file">
          <img v-if="f.kind === 'image'" class="shot" :src="fileUrl(f)" :alt="f.name" />
          <audio v-else-if="f.kind === 'audio'" controls :src="fileUrl(f)" />
          <video v-else-if="f.kind === 'video'" controls :src="fileUrl(f)" />
          <span v-else-if="f.kind === 'blocked'">[blocked file]</span>
          <span v-else-if="f.kind === 'missing'">[missing file]</span>
          <a v-else :href="fileUrl(f)" download>📎 {{ f.name }}</a>
        </div>
        <slot name="footer" :message="m" />
      </div>
    </template>
    <div v-if="live" class="msg msg-assistant">
      <div class="msg-meta">
        <span class="avatar avatar-agent" aria-hidden="true">T</span>
        <span>Tether</span>
      </div>
      <div v-if="live.tools.length > 0" class="tools">
        <ToolLine
          v-for="t in live.tools"
          :key="t.call"
          :tool="t"
          :selected="selected === t.call"
          @select="emit('tool', t)"
        />
      </div>
      <StreamText v-if="live.text !== ''" :text="live.text" :running="live.running" />
      <div v-else-if="live.running && live.tools.length === 0" class="working">
        <span class="dots"><i></i><i></i><i></i></span>
        working
      </div>
      <slot name="live" />
    </div>
    <slot v-else name="live" />
  </div>
</template>

<style scoped>
.is-compacted {
  opacity: 0.6;
}
</style>
