<script setup lang="ts">
import { Alert, AlertDescription } from "@/components/ui/alert"
import { Button } from "@/components/ui/button"
import { reloadNow } from "@/lib/update"
import { ui } from "@/stores/ui"
</script>

<template>
  <Alert v-if="ui.updateReady" role="status" class="rounded-none border-x-0 border-t-0 py-1.5">
    <AlertDescription class="col-start-2 flex flex-wrap items-center justify-center gap-2">
      <span>A new version is ready · </span>
      <Button variant="secondary" size="sm" @click="reloadNow()">Reload</Button>
    </AlertDescription>
  </Alert>
</template>
