<script setup lang="ts">
import { computed } from "vue";
import { CircleAlert, CircleCheck, Info, TriangleAlert } from "@lucide/vue";
import { Alert, AlertDescription } from "@/components/ui/alert";
import type { Notice } from "@/composables/useFlasher";

const props = defineProps<{ notice: Notice }>();
const icon = computed(() => ({ ok: CircleCheck, warn: TriangleAlert, error: CircleAlert, info: Info })[props.notice.tone]);
const toneClass = computed(
  () =>
    ({
      ok: "border-success/40 text-success *:data-[slot=alert-description]:text-success/90",
      warn: "border-warning/40 text-warning *:data-[slot=alert-description]:text-warning/90",
      error: "border-destructive/40",
      info: "",
    })[props.notice.tone],
);
</script>

<template>
  <Alert :variant="notice.tone === 'error' ? 'destructive' : 'default'" :class="toneClass">
    <component :is="icon" />
    <AlertDescription class="whitespace-pre-line">{{ notice.text }}</AlertDescription>
  </Alert>
</template>
