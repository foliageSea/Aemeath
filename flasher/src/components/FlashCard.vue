<script setup lang="ts">
import { FileUp, LoaderCircle, Package, ShieldCheck, Trash2, TriangleAlert, Zap } from "@lucide/vue";
import { computed, ref } from "vue";
import NoticeAlert from "@/components/NoticeAlert.vue";
import StepTitle from "@/components/StepTitle.vue";
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@/components/ui/alert-dialog";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardFooter, CardHeader, CardTitle } from "@/components/ui/card";
import { Checkbox } from "@/components/ui/checkbox";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Progress } from "@/components/ui/progress";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { fmtBytes, useFlasher } from "@/composables/useFlasher";
import { hex, parseOffset } from "@/localFiles";
import { cn } from "@/lib/utils";

const f = useFlasher();
const confirmOpen = ref(false);
const dragging = ref(false);
const flashing = computed(() => f.busy.value === "flashing");
const pct = computed(() => Math.round(f.progress.fraction * 1000) / 10);
const m = computed(() => f.manifest.value);
const total = computed(() => m.value?.parts.reduce((n, p) => n + p.size, 0) ?? 0);
const flashLabel = computed(() =>
  flashing.value ? "刷入中…" : !f.connected.value ? "请先连接设备" : f.chipError.value ? "设备不兼容" : "开始刷入",
);

function onFlashClick() {
  if (f.eraseAll.value) confirmOpen.value = true;
  else void f.flash();
}
function onPick(e: Event) {
  const input = e.target as HTMLInputElement;
  if (input.files) f.addLocalFiles(input.files);
  input.value = "";
}
function onDrop(e: DragEvent) {
  dragging.value = false;
  if (e.dataTransfer?.files.length) f.addLocalFiles(e.dataTransfer.files);
}
</script>

<template>
  <Card>
    <CardHeader>
      <CardTitle><StepTitle :n="2" title="刷入固件" :done="f.notices.flash?.tone === 'ok'" :active="f.step.value === 2" /></CardTitle>
      <CardDescription>默认只写入固件区域，保留 NVS 中的 Wi-Fi 配网与屏幕亮度设置。</CardDescription>
    </CardHeader>

    <CardContent class="flex flex-col gap-4">
      <Tabs v-model="f.mode.value">
        <TabsList class="w-full">
          <TabsTrigger value="release" :disabled="flashing || !m"><Package />发布固件</TabsTrigger>
          <TabsTrigger value="local" :disabled="flashing"><FileUp />本地文件</TabsTrigger>
        </TabsList>

        <TabsContent value="release" class="mt-3">
          <div v-if="f.manifestState.value === 'loading'" class="flex items-center gap-2 py-6 text-sm text-muted-foreground">
            <LoaderCircle class="size-4 animate-spin" />正在读取固件信息…
          </div>
          <div v-else-if="m" class="flex flex-col gap-3">
            <div class="grid grid-cols-2 gap-2 sm:grid-cols-4">
              <div class="rounded-lg border p-3">
                <div class="text-xs text-muted-foreground">版本</div>
                <div class="mt-0.5 font-semibold">v{{ m.version }}</div>
              </div>
              <div class="rounded-lg border p-3">
                <div class="text-xs text-muted-foreground">大小</div>
                <div class="mt-0.5 font-semibold">{{ fmtBytes(total) }}</div>
              </div>
              <div class="rounded-lg border p-3">
                <div class="text-xs text-muted-foreground">ESP-IDF</div>
                <div class="mt-0.5 font-semibold">{{ m.idfVersion ?? "—" }}</div>
              </div>
              <div class="rounded-lg border p-3">
                <div class="text-xs text-muted-foreground">Flash</div>
                <div class="mt-0.5 font-semibold">{{ m.flash.size }} · {{ m.flash.mode }}</div>
              </div>
            </div>
            <div class="overflow-hidden rounded-lg border">
              <Table>
                <TableHeader>
                  <TableRow class="bg-muted/40 hover:bg-muted/40">
                    <TableHead>分区</TableHead><TableHead>偏移</TableHead><TableHead>文件</TableHead><TableHead class="text-right">大小</TableHead>
                  </TableRow>
                </TableHeader>
                <TableBody>
                  <TableRow v-for="p in m.parts" :key="p.path">
                    <TableCell class="font-medium">{{ p.name }}</TableCell>
                    <TableCell class="font-mono text-xs">{{ hex(p.offset) }}</TableCell>
                    <TableCell class="font-mono text-xs">{{ p.path }}</TableCell>
                    <TableCell class="text-right tabular-nums">{{ fmtBytes(p.size) }}</TableCell>
                  </TableRow>
                </TableBody>
              </Table>
            </div>
            <p class="flex items-center gap-1.5 text-xs text-muted-foreground">
              <ShieldCheck class="size-3.5" />构建于 {{ new Date(m.builtAt).toLocaleString() }} · 下载后校验 SHA-256，写入后回读 MD5
            </p>
          </div>
          <NoticeAlert v-else :notice="f.notices.manifest ?? { tone: 'warn', text: '此站点未附带发布固件，请使用“本地文件”刷入。' }" />
        </TabsContent>

        <TabsContent value="local" class="mt-3 flex flex-col gap-3">
          <label
            :class="
              cn(
                'flex cursor-pointer flex-col items-center justify-center gap-1.5 rounded-lg border border-dashed px-4 py-6 text-center text-sm transition-colors hover:bg-muted/50',
                dragging && 'border-primary bg-primary/5',
                flashing && 'pointer-events-none opacity-50',
              )
            "
            @dragover.prevent="dragging = true"
            @dragleave="dragging = false"
            @drop.prevent="onDrop"
          >
            <FileUp class="size-6 text-muted-foreground" />
            <span class="font-medium">点击或拖入 .bin 文件</span>
            <span class="text-xs text-muted-foreground">自动推断偏移：bootloader → 0x0，partition-table → 0x8000，其他 → 0x10000；合并镜像请填 0x0</span>
            <input type="file" accept=".bin,application/octet-stream" multiple class="sr-only" :disabled="flashing" @change="onPick" />
          </label>
          <div v-if="f.local.value.length" class="overflow-hidden rounded-lg border">
            <Table>
              <TableHeader>
                <TableRow class="bg-muted/40 hover:bg-muted/40">
                  <TableHead>文件</TableHead><TableHead class="w-36">偏移</TableHead><TableHead class="text-right">大小</TableHead><TableHead class="w-10" />
                </TableRow>
              </TableHeader>
              <TableBody>
                <TableRow v-for="l in f.local.value" :key="l.id">
                  <TableCell class="max-w-48 truncate font-mono text-xs" :title="l.file.name">{{ l.file.name }}</TableCell>
                  <TableCell>
                    <Input
                      v-model="l.offsetText"
                      class="h-8 font-mono text-xs"
                      :aria-invalid="parseOffset(l.offsetText) === null"
                      :disabled="flashing"
                    />
                  </TableCell>
                  <TableCell class="text-right tabular-nums">{{ fmtBytes(l.file.size) }}</TableCell>
                  <TableCell>
                    <Button variant="ghost" size="icon-sm" aria-label="移除" :disabled="flashing" @click="f.removeLocal(l.id)"><Trash2 /></Button>
                  </TableCell>
                </TableRow>
              </TableBody>
            </Table>
          </div>
          <ul v-if="f.localErrors.value.length" class="list-disc space-y-0.5 pl-5 text-sm text-destructive">
            <li v-for="e in f.localErrors.value" :key="e">{{ e }}</li>
          </ul>
        </TabsContent>
      </Tabs>

      <div v-if="f.progress.visible" class="flex flex-col gap-2 rounded-lg border bg-muted/30 p-3">
        <div class="flex items-center justify-between text-sm">
          <span class="flex items-center gap-2">
            <LoaderCircle v-if="flashing" class="size-4 animate-spin text-primary" />{{ f.progress.text }}
          </span>
          <span class="font-mono tabular-nums text-muted-foreground">{{ pct.toFixed(1) }}%</span>
        </div>
        <Progress :model-value="pct" />
      </div>

      <NoticeAlert v-if="f.notices.flash" :notice="f.notices.flash" />
    </CardContent>

    <CardFooter class="flex flex-wrap items-center gap-3 border-t">
      <div class="flex items-center gap-2">
        <Checkbox id="erase-all" v-model="f.eraseAll.value" :disabled="flashing" />
        <Label for="erase-all" class="font-normal" :class="f.eraseAll.value && 'text-destructive'">刷入前整片擦除</Label>
      </div>
      <Button class="ml-auto min-w-36" :disabled="!f.canFlash.value" @click="onFlashClick">
        <LoaderCircle v-if="flashing" class="animate-spin" />
        <Zap v-else />{{ flashLabel }}
      </Button>
    </CardFooter>
  </Card>

  <AlertDialog v-model:open="confirmOpen">
    <AlertDialogContent>
      <AlertDialogHeader>
        <AlertDialogTitle class="flex items-center gap-2"><TriangleAlert class="size-5 text-destructive" />确认整片擦除？</AlertDialogTitle>
        <AlertDialogDescription>
          整片擦除会清除 Wi-Fi 配网、屏幕亮度等全部 NVS 数据，并需要额外约 30 秒。刷入后设备会进入配网模式，需要重新配网。
        </AlertDialogDescription>
      </AlertDialogHeader>
      <AlertDialogFooter>
        <AlertDialogCancel>取消</AlertDialogCancel>
        <AlertDialogAction variant="destructive" @click="f.flash()">擦除并刷入</AlertDialogAction>
      </AlertDialogFooter>
    </AlertDialogContent>
  </AlertDialog>
</template>
