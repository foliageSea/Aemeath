<script setup lang="ts">
import { ChevronDown, Eraser, TerminalSquare } from "@lucide/vue";
import { nextTick, ref, useTemplateRef, watch } from "vue";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from "@/components/ui/collapsible";
import { useFlasher } from "@/composables/useFlasher";

const f = useFlasher();
const open = ref(false);
const term = useTemplateRef<HTMLPreElement>("term");
watch(f.esptoolLog, async () => {
  await nextTick();
  if (term.value) term.value.scrollTop = term.value.scrollHeight;
});
</script>

<template>
  <Card class="gap-0 py-0">
    <Collapsible v-model:open="open">
      <div class="flex items-center gap-2 px-4 py-2">
        <CollapsibleTrigger as-child>
          <Button variant="ghost" size="sm" class="-ml-2 font-medium">
            <TerminalSquare />刷写详细输出（esptool）
            <ChevronDown :class="['transition-transform', open && 'rotate-180']" />
          </Button>
        </CollapsibleTrigger>
        <Button v-if="open" variant="ghost" size="icon-sm" class="ml-auto" aria-label="清空" @click="f.clearEsptool"><Eraser /></Button>
      </div>
      <CollapsibleContent>
        <pre
          ref="term"
          class="mx-4 mb-4 h-56 overflow-auto rounded-lg border bg-zinc-950 p-3 font-mono text-xs leading-relaxed break-all whitespace-pre-wrap text-zinc-300"
        >{{ f.esptoolLog.value || "暂无输出" }}</pre>
      </CollapsibleContent>
    </Collapsible>
  </Card>
</template>
