<script setup lang="ts">
import { nextTick, ref, watch } from "vue"
import { api } from "../api/client"
import type { SkillFileView } from "../api/types.gen"
import { friendlyError } from "../lib/errors"
import StateBlock from "./StateBlock.vue"

const props = defineProps<{ skill: string; path: string; line: number | null }>()

const state = ref<"loading" | "ready" | "error">("loading")
const lines = ref<string[]>([])
const cut = ref(false)
const errorText = ref("")
const box = ref<HTMLElement | null>(null)
let request = 0

// Reads the file. A late reply for an old path is dropped.
async function load(): Promise<void> {
  const mine = ++request
  state.value = "loading"
  try {
    const r = await api<SkillFileView>(
      "GET",
      `/api/skills/${encodeURIComponent(props.skill)}/file?path=${encodeURIComponent(props.path)}`,
    )
    if (mine !== request) return
    lines.value = r.text.split("\n")
    cut.value = r.cut
    state.value = "ready"
    await nextTick()
    if (props.line !== null)
      box.value?.querySelector(`li[data-n="${props.line}"]`)?.scrollIntoView({
        block: "center",
        behavior: "auto",
      })
  } catch (e) {
    if (mine !== request) return
    errorText.value = friendlyError(e)
    state.value = "error"
  }
}

watch(() => [props.skill, props.path, props.line], load, { immediate: true })
</script>

<template>
  <StateBlock v-if="state === 'loading'" state="loading" />
  <StateBlock
    v-else-if="state === 'error'"
    state="error"
    title="Could not open the file"
    :text="errorText"
  />
  <template v-else>
    <p v-if="cut" class="text-xs text-muted-foreground">The file is cut at 256 KiB.</p>
    <ol ref="box" class="overflow-auto rounded-md border bg-muted/30 py-2 font-mono text-xs">
      <li
        v-for="(l, i) in lines"
        :key="i"
        :data-n="i + 1"
        :data-hit="i + 1 === line || undefined"
        :aria-current="i + 1 === line || undefined"
        class="whitespace-pre pl-12 pr-3 before:-ml-10 before:inline-block before:w-8 before:pr-2 before:text-right before:text-muted-foreground before:content-[attr(data-n)] data-[hit=true]:bg-warning/15"
        v-text="l"
      ></li>
    </ol>
  </template>
</template>
