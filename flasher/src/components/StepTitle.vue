<script setup lang="ts">
import { Check } from "@lucide/vue";
import { cn } from "@/lib/utils";

defineProps<{ n: number; title: string; done?: boolean; active?: boolean }>();
</script>

<template>
  <span class="flex items-center gap-2.5">
    <span
      :class="
        cn(
          'flex size-6 shrink-0 items-center justify-center rounded-full border text-xs font-semibold transition-colors',
          done ? 'border-success bg-success text-white' : active ? 'border-primary bg-primary text-primary-foreground' : 'text-muted-foreground',
        )
      "
    >
      <Check v-if="done" class="size-3.5" />
      <template v-else>{{ n }}</template>
    </span>
    {{ title }}
  </span>
</template>
