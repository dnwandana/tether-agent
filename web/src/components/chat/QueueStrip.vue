<script setup lang="ts">
import type { QueueItem } from "../../api/types.gen"
import GlyphText from "@/components/GlyphText.vue"
import { Button } from "@/components/ui/button"
import {
  Item,
  ItemActions,
  ItemContent,
  ItemDescription,
  ItemMedia,
  ItemTitle,
} from "@/components/ui/item"

withDefaults(defineProps<{ items: QueueItem[]; max?: number }>(), { max: 20 })
const emit = defineEmits<{ now: [] }>()
</script>

<template>
  <div v-if="items.length > 0" class="flex flex-col gap-1">
    <Item variant="muted" size="sm">
      <ItemContent>
        <ItemTitle>Waiting · {{ items.length }} / {{ max }}</ItemTitle>
        <ItemDescription class="truncate">
          They run after the current task, one at a time.
        </ItemDescription>
      </ItemContent>
      <ItemActions>
        <Button type="button" variant="secondary" size="sm" @click="emit('now')">
          Do this now
        </Button>
      </ItemActions>
    </Item>
    <ol class="flex flex-col gap-1">
      <Item v-for="(it, i) in items" :key="i" as="li" size="sm">
        <ItemMedia>{{ i + 1 }}</ItemMedia>
        <ItemContent>
          <GlyphText v-if="it.kind === 'goal'" :text="it.preview" truncate />
          <span v-else class="truncate">{{ it.preview }}</span>
        </ItemContent>
      </Item>
    </ol>
  </div>
</template>
