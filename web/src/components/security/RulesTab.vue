<script setup lang="ts">
import { computed } from "vue"
import type { SecurityFixed } from "../../api/types.gen"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card"
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from "@/components/ui/collapsible"
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
  <div class="flex flex-col gap-4">
    <Card>
      <CardHeader>
        <CardTitle>Process mode</CardTitle>
        <CardDescription>Set in <code>approvals.mode</code>.</CardDescription>
      </CardHeader>
      <CardContent>
        <ul class="flex flex-col gap-1 text-sm">
          <li v-for="[k, text] in PROCESS" :key="k">
            <code class="font-mono">{{ k }}</code>
            {{ " " }}<Badge v-if="k === processMode" variant="secondary">now</Badge> {{ " "
            }}{{ text }}
          </li>
        </ul>
      </CardContent>
    </Card>
    <Card>
      <CardHeader>
        <CardTitle>Cron mode</CardTitle>
        <CardDescription>Set in <code>approvals.cron_mode</code>.</CardDescription>
      </CardHeader>
      <CardContent>
        <ul class="flex flex-col gap-1 text-sm">
          <li v-for="[k, text] in CRON" :key="k">
            <code class="font-mono">{{ k }}</code>
            {{ " " }}<Badge v-if="k === cronMode" variant="secondary">now</Badge> {{ " "
            }}{{ text }}
          </li>
        </ul>
      </CardContent>
    </Card>
    <Card>
      <CardHeader>
        <CardTitle>Protected paths</CardTitle>
      </CardHeader>
      <CardContent>
        <Collapsible v-if="fixed.protected_paths.length">
          <CollapsibleTrigger as-child>
            <Button variant="ghost" size="sm">
              {{ fixed.protected_paths.length }}
              {{ fixed.protected_paths.length === 1 ? "path" : "paths" }}
            </Button>
          </CollapsibleTrigger>
          <CollapsibleContent>
            <pre class="rounded-md bg-muted p-2 font-mono text-xs break-all whitespace-pre-wrap">{{
              fixed.protected_paths.join("\n")
            }}</pre>
          </CollapsibleContent>
        </Collapsible>
        <p v-else class="text-sm text-muted-foreground">None.</p>
      </CardContent>
    </Card>
    <Card>
      <CardHeader>
        <CardTitle>Human-only rules</CardTitle>
      </CardHeader>
      <CardContent>
        <ul v-if="fixed.human_only.length" class="flex flex-col gap-1 text-sm">
          <li v-for="r in fixed.human_only" :key="r">🔒 {{ r }}</li>
        </ul>
        <p v-else class="text-sm text-muted-foreground">None.</p>
      </CardContent>
    </Card>
    <Card>
      <CardHeader>
        <CardTitle>MCP classes</CardTitle>
      </CardHeader>
      <CardContent>
        <DataTable v-if="mcpRows.length" :columns="mcpCols" :rows="mcpRows" row-key="key" />
        <p v-else class="text-sm text-muted-foreground">None.</p>
      </CardContent>
    </Card>
    <Card>
      <CardHeader>
        <CardTitle>ACP presets</CardTitle>
      </CardHeader>
      <CardContent>
        <ul v-if="fixed.acp_presets.length" class="flex flex-col gap-1 text-sm">
          <li v-for="p in fixed.acp_presets" :key="p">{{ p }}</li>
        </ul>
        <p v-else class="text-sm text-muted-foreground">None.</p>
      </CardContent>
    </Card>
    <Card>
      <CardHeader>
        <CardTitle>ACP work folders</CardTitle>
      </CardHeader>
      <CardContent>
        <pre
          v-if="fixed.acp_workdirs.length"
          class="rounded-md bg-muted p-2 font-mono text-xs break-all whitespace-pre-wrap"
          >{{ fixed.acp_workdirs.join("\n") }}</pre>
        <p v-else class="text-sm text-muted-foreground">None.</p>
      </CardContent>
    </Card>
    <Card>
      <CardHeader>
        <CardTitle>Toolsets</CardTitle>
      </CardHeader>
      <CardContent>
        <ul v-if="Object.keys(fixed.toolsets).length" class="flex flex-col gap-1 text-sm">
          <li v-for="(sets, ch) in fixed.toolsets" :key="ch">
            <b>{{ ch }}</b
            >: {{ sets.join(", ") }}
          </li>
        </ul>
        <p v-else class="text-sm text-muted-foreground">None.</p>
      </CardContent>
    </Card>
  </div>
</template>
