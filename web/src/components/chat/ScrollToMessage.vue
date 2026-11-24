<script setup lang="ts">
import { onMounted, watch } from "vue"
import { useMessageScroller } from "@/components/ui/message-scroller"

const props = defineProps<{ messageId: string }>()

// The page renders the provider, so this component calls the scroller from inside the provider.
const { scrollToMessage } = useMessageScroller()

// Scrolls the message to the center of the viewport when the id is not empty.
function jump(): void {
  if (props.messageId !== "") scrollToMessage(props.messageId, { align: "center" })
}

onMounted(jump)
watch(() => props.messageId, jump)
</script>

<template>
  <span hidden />
</template>
