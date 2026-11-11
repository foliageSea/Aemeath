<script setup lang="ts">
import { useColorMode } from "@vueuse/core";
import { Monitor, Moon, Sun } from "@lucide/vue";
import { computed } from "vue";
import { Button } from "@/components/ui/button";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";

const { store } = useColorMode({ storageKey: "aemeath-flasher-theme" });
const order = ["auto", "light", "dark"] as const;
const labels = { auto: "跟随系统", light: "浅色", dark: "深色" } as const;
const current = computed(() => (order.includes(store.value as never) ? (store.value as (typeof order)[number]) : "auto"));
function cycle() {
  store.value = order[(order.indexOf(current.value) + 1) % order.length];
}
</script>

<template>
  <Tooltip>
    <TooltipTrigger as-child>
      <Button variant="ghost" size="icon" :aria-label="`主题：${labels[current]}`" @click="cycle">
        <Sun v-if="current === 'light'" />
        <Moon v-else-if="current === 'dark'" />
        <Monitor v-else />
      </Button>
    </TooltipTrigger>
    <TooltipContent>主题：{{ labels[current] }}</TooltipContent>
  </Tooltip>
</template>
