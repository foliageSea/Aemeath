<script setup lang="ts">
import { ChevronDown, LoaderCircle, Plug, Unplug, Usb } from "@lucide/vue";
import { ref } from "vue";
import NoticeAlert from "@/components/NoticeAlert.vue";
import StepTitle from "@/components/StepTitle.vue";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardAction, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Checkbox } from "@/components/ui/checkbox";
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from "@/components/ui/collapsible";
import { Label } from "@/components/ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { BAUD_RATES, useFlasher } from "@/composables/useFlasher";

const f = useFlasher();
const helpOpen = ref(false);
</script>

<template>
  <Card>
    <CardHeader>
      <CardTitle><StepTitle :n="1" title="连接设备" :done="f.connected.value && !f.chipError.value" :active="f.step.value === 1" /></CardTitle>
      <CardDescription>用 USB-C 数据线连接开发板，在弹窗中选择 ESP32-S3（USB JTAG/serial debug unit）。</CardDescription>
      <CardAction>
        <Badge v-if="f.connected.value" variant="outline" class="border-success/40 text-success">
          <span class="size-1.5 rounded-full bg-success" />已连接
        </Badge>
        <Badge v-else variant="outline" class="text-muted-foreground">
          <span class="size-1.5 rounded-full bg-muted-foreground/50" />未连接
        </Badge>
      </CardAction>
    </CardHeader>
    <CardContent class="flex flex-col gap-4">
      <div class="flex flex-wrap items-end gap-4">
        <div class="grid gap-1.5">
          <Label for="baud" class="text-xs text-muted-foreground">波特率</Label>
          <Select v-model="f.baud.value" :disabled="f.connected.value || f.busy.value !== null">
            <SelectTrigger id="baud" class="w-40"><SelectValue /></SelectTrigger>
            <SelectContent>
              <SelectItem v-for="b in BAUD_RATES" :key="b" :value="String(b)">
                {{ b }}<span v-if="b === 115200" class="text-muted-foreground">（推荐）</span>
              </SelectItem>
            </SelectContent>
          </Select>
        </div>
        <div class="flex h-9 items-center gap-2">
          <Checkbox id="all-ports" v-model="f.allPorts.value" :disabled="f.busy.value !== null" />
          <Label for="all-ports" class="font-normal">显示所有串口</Label>
        </div>
        <div class="ml-auto">
          <Button v-if="!f.connected.value" :disabled="!f.supported || f.busy.value !== null || f.monitor.value !== null" @click="f.connect">
            <LoaderCircle v-if="f.busy.value === 'connecting'" class="animate-spin" />
            <Plug v-else />
            {{ f.busy.value === "connecting" ? "连接中…" : "连接设备" }}
          </Button>
          <Button v-else variant="outline" :disabled="f.busy.value !== null" @click="f.disconnect">
            <Unplug />断开并重启
          </Button>
        </div>
      </div>

      <dl v-if="f.chip.value" class="grid grid-cols-[auto_1fr] gap-x-6 gap-y-1.5 rounded-lg border bg-muted/40 p-3 text-sm">
        <dt class="text-muted-foreground">芯片</dt>
        <dd class="font-mono break-all">{{ f.chip.value.description }}</dd>
        <dt class="text-muted-foreground">MAC</dt>
        <dd class="font-mono">{{ f.chip.value.mac || "未知" }}</dd>
        <dt class="text-muted-foreground">Flash</dt>
        <dd class="font-mono">{{ f.chip.value.flashSize ?? "未知" }}</dd>
      </dl>

      <NoticeAlert v-if="f.notices.connect" :notice="f.notices.connect" />

      <Collapsible v-model:open="helpOpen">
        <CollapsibleTrigger as-child>
          <Button variant="link" size="sm" class="h-auto px-0 text-muted-foreground">
            <Usb />连接失败？手动进入下载模式
            <ChevronDown :class="['transition-transform', helpOpen && 'rotate-180']" />
          </Button>
        </CollapsibleTrigger>
        <CollapsibleContent>
          <div class="mt-2 rounded-lg border border-dashed p-3 text-sm text-muted-foreground">
            <ol class="list-decimal space-y-1 pl-5">
              <li>按住板上的 <b class="text-foreground">BOOT</b> 键；</li>
              <li>短按一下 <b class="text-foreground">RESET（RST）</b> 键；</li>
              <li>松开 <b class="text-foreground">BOOT</b>，再点击“连接设备”。</li>
            </ol>
            <p class="mt-2">同时请关闭 <code class="rounded bg-muted px-1 font-mono text-xs">idf.py monitor</code>、串口助手等占用串口的程序，并使用支持数据传输的 USB 线。</p>
          </div>
        </CollapsibleContent>
      </Collapsible>
    </CardContent>
  </Card>
</template>
