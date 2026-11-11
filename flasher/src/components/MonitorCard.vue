<script setup lang="ts">
import { Download, Eraser, LoaderCircle, Play, RotateCcw, Square } from "@lucide/vue";
import { nextTick, ref, useTemplateRef, watch } from "vue";
import NoticeAlert from "@/components/NoticeAlert.vue";
import StepTitle from "@/components/StepTitle.vue";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardAction, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Checkbox } from "@/components/ui/checkbox";
import { Label } from "@/components/ui/label";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import { useFlasher } from "@/composables/useFlasher";

const f = useFlasher();
const autoscroll = ref(true);
const term = useTemplateRef<HTMLPreElement>("term");
watch(f.monitorLog, async () => {
  if (!autoscroll.value) return;
  await nextTick();
  if (term.value) term.value.scrollTop = term.value.scrollHeight;
});
</script>

<template>
  <Card class="flex min-h-0 min-w-0 flex-col">
    <CardHeader>
      <CardTitle><StepTitle :n="3" title="串口日志" :active="f.step.value === 3" /></CardTitle>
      <CardDescription>以 115200 读取设备日志，确认刷入后的固件正常启动。</CardDescription>
      <CardAction>
        <Badge v-if="f.monitor.value" variant="outline" class="border-success/40 text-success">
          <span class="size-1.5 animate-pulse rounded-full bg-success" />监听中
        </Badge>
      </CardAction>
    </CardHeader>
    <CardContent class="flex min-h-0 flex-1 flex-col gap-3">
      <div class="flex flex-wrap items-center gap-2">
        <Button v-if="!f.monitor.value" size="sm" :disabled="!f.supported || f.busy.value !== null" @click="f.startMonitor">
          <LoaderCircle v-if="f.busy.value === 'monitor-opening'" class="animate-spin" /><Play v-else />打开日志
        </Button>
        <Button v-else size="sm" variant="outline" @click="f.stopMonitor"><Square />停止</Button>
        <Tooltip>
          <TooltipTrigger as-child>
            <Button size="sm" variant="outline" :disabled="!f.monitor.value" @click="f.resetDevice"><RotateCcw />复位</Button>
          </TooltipTrigger>
          <TooltipContent>通过 RTS 复位芯片，重新查看启动日志</TooltipContent>
        </Tooltip>
        <div class="ml-auto flex items-center gap-1">
          <div class="mr-2 flex items-center gap-2">
            <Checkbox id="autoscroll" v-model="autoscroll" />
            <Label for="autoscroll" class="text-xs font-normal">自动滚动</Label>
          </div>
          <Tooltip>
            <TooltipTrigger as-child>
              <Button size="icon-sm" variant="ghost" aria-label="清空" @click="f.clearMonitor"><Eraser /></Button>
            </TooltipTrigger>
            <TooltipContent>清空</TooltipContent>
          </Tooltip>
          <Tooltip>
            <TooltipTrigger as-child>
              <Button size="icon-sm" variant="ghost" aria-label="下载日志" @click="f.downloadMonitorLog"><Download /></Button>
            </TooltipTrigger>
            <TooltipContent>下载日志</TooltipContent>
          </Tooltip>
        </div>
      </div>
      <NoticeAlert v-if="f.notices.monitor" :notice="f.notices.monitor" />
      <pre
        ref="term"
        class="h-80 min-h-0 flex-none overflow-auto overscroll-contain rounded-lg border bg-zinc-950 p-3 font-mono text-xs leading-relaxed break-all whitespace-pre-wrap text-zinc-200 lg:h-0 lg:flex-1"
      ><template v-if="f.monitorLog.value">{{ f.monitorLog.value }}</template><span v-else class="text-zinc-500">{{ f.monitor.value ? "等待设备输出…" : "日志未打开。刷入完成后点击“打开日志”查看设备启动输出。" }}</span></pre>
    </CardContent>
  </Card>
</template>
