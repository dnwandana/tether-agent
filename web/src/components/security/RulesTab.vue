<script setup lang="ts">
import { computed } from "vue"
import type { SecurityFixed } from "../../api/types.gen"
import DataTable from "../DataTable.vue"

const props = defineProps<{ fixed: SecurityFixed; processMode: string; cronMode: string }>()

const PROCESS: [string, string][] = [
  ["manual", "The gate asks you about each dangerous command."],
  [
    "smart",
    "A model call judges the risk. A low-risk command runs with no question. Other commands ask you.",
  ],
  [
    "off",
    "Dangerous commands run with no question. The hardline list, the sudo guard, and the human-only rules still apply.",
  ],
]
const CRON: [string, string][] = [
  ["deny", "A cron run refuses a dangerous command with no question."],
  ["approve", "A cron run lets a dangerous command run."],
]

const mcpRows = computed(() => {
  const out: { key: string; server: string; tool: string; class: string }[] = []
  for (const [server, classes] of Object.entries(props.fixed.mcp_classes ?? {}))
    for (const [cls, n] of Object.entries(classes))
      out.push({
        key: server + ":" + cls,
        server,
        tool: n === 1 ? "1 tool" : `${n} tools`,
        class: cls,
      })
  return out as Record<string, unknown>[]
})
const mcpCols = [
  { key: "server", label: "Server" },
  { key: "tool", label: "Tools" },
  { key: "class", label: "Class" },
]
</script>

<template>
  <div class="page-stack">
    <div class="card card-pad">
      <h3 class="h">Process mode</h3>
      <p class="t-12 c-3">Set in <code>approvals.mode</code>.</p>
      <ul class="list">
        <li v-for="[k, text] in PROCESS" :key="k">
          <code>{{ k }}</code>
          {{ " " }}<span v-if="k === processMode" class="chip chip-acc">now</span>
          {{ text }}
        </li>
      </ul>
    </div>
    <div class="card card-pad">
      <h3 class="h">Cron mode</h3>
      <p class="t-12 c-3">Set in <code>approvals.cron_mode</code>.</p>
      <ul class="list">
        <li v-for="[k, text] in CRON" :key="k">
          <code>{{ k }}</code>
          {{ " " }}<span v-if="k === cronMode" class="chip chip-acc">now</span>
          {{ text }}
        </li>
      </ul>
    </div>
    <div class="card card-pad">
      <h3 class="h">Protected paths</h3>
      <details v-if="fixed.protected_paths.length">
        <summary>
          {{ fixed.protected_paths.length }}
          {{ fixed.protected_paths.length === 1 ? "path" : "paths" }}
        </summary>
        <div class="code-block">{{ fixed.protected_paths.join("\n") }}</div>
      </details>
      <p v-else class="c-3">None.</p>
    </div>
    <div class="card card-pad">
      <h3 class="h">Human-only rules</h3>
      <ul v-if="fixed.human_only.length" class="list">
        <li v-for="r in fixed.human_only" :key="r">🔒 {{ r }}</li>
      </ul>
      <p v-else class="c-3">None.</p>
    </div>
    <div class="card card-pad">
      <h3 class="h">MCP classes</h3>
      <DataTable v-if="mcpRows.length" :columns="mcpCols" :rows="mcpRows" row-key="key" />
      <p v-else class="c-3">None.</p>
    </div>
    <div class="card card-pad">
      <h3 class="h">ACP presets</h3>
      <ul v-if="fixed.acp_presets.length" class="list">
        <li v-for="p in fixed.acp_presets" :key="p">{{ p }}</li>
      </ul>
      <p v-else class="c-3">None.</p>
    </div>
    <div class="card card-pad">
      <h3 class="h">ACP work folders</h3>
      <div v-if="fixed.acp_workdirs.length" class="code-block">
        {{ fixed.acp_workdirs.join("\n") }}
      </div>
      <p v-else class="c-3">None.</p>
    </div>
    <div class="card card-pad">
      <h3 class="h">Toolsets</h3>
      <ul v-if="Object.keys(fixed.toolsets).length" class="list">
        <li v-for="(sets, ch) in fixed.toolsets" :key="ch">
          <b>{{ ch }}</b
          >: {{ sets.join(", ") }}
        </li>
      </ul>
      <p v-else class="c-3">None.</p>
    </div>
  </div>
</template>
