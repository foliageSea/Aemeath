<script setup lang="ts">
import { CircleAlert, Cpu } from "@lucide/vue";
import { onMounted } from "vue";
import ConnectCard from "@/components/ConnectCard.vue";
import EsptoolLogCard from "@/components/EsptoolLogCard.vue";
import FlashCard from "@/components/FlashCard.vue";
import MonitorCard from "@/components/MonitorCard.vue";
import ThemeToggle from "@/components/ThemeToggle.vue";
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { TooltipProvider } from "@/components/ui/tooltip";
import { useFlasher } from "@/composables/useFlasher";

const f = useFlasher();
onMounted(() => void f.loadManifest());
</script>

<template>
  <TooltipProvider :delay-duration="300">
    <div class="min-h-svh bg-muted/30">
      <header class="sticky top-0 z-10 border-b bg-background/80 backdrop-blur">
        <div class="mx-auto flex h-14 max-w-6xl items-center gap-3 px-4">
          <div class="flex size-8 items-center justify-center rounded-lg bg-primary text-primary-foreground">
            <Cpu class="size-4.5" />
          </div>
          <div class="leading-tight">
            <div class="font-semibold">Aemeath 网页刷机</div>
            <div class="hidden text-xs text-muted-foreground sm:block">Waveshare ESP32-S3-Touch-LCD-4B</div>
          </div>
          <div class="ml-auto flex items-center gap-1">
            <Badge v-if="f.manifest.value" variant="secondary" class="mr-1 font-mono">固件 v{{ f.manifest.value.version }}</Badge>
            <Button variant="ghost" size="icon" as-child>
              <a href="https://github.com/foliageSea/Aemeath" target="_blank" rel="noopener" aria-label="GitHub">
                <svg viewBox="0 0 24 24" fill="currentColor" aria-hidden="true"><path d="M12 .5a11.5 11.5 0 0 0-3.64 22.41c.58.1.79-.25.79-.56v-2c-3.2.7-3.88-1.37-3.88-1.37-.53-1.33-1.29-1.69-1.29-1.69-1.05-.72.08-.7.08-.7 1.16.08 1.77 1.19 1.77 1.19 1.03 1.77 2.71 1.26 3.37.96.1-.75.4-1.26.73-1.55-2.55-.29-5.24-1.28-5.24-5.69 0-1.26.45-2.29 1.19-3.1-.12-.29-.52-1.46.11-3.05 0 0 .97-.31 3.17 1.18a11 11 0 0 1 5.77 0c2.2-1.49 3.17-1.18 3.17-1.18.63 1.59.23 2.76.11 3.05.74.81 1.19 1.84 1.19 3.1 0 4.42-2.7 5.39-5.26 5.68.41.36.78 1.06.78 2.14v3.17c0 .31.21.67.8.56A11.5 11.5 0 0 0 12 .5Z" /></svg>
              </a>
            </Button>
            <ThemeToggle />
          </div>
        </div>
      </header>

      <main class="mx-auto flex max-w-6xl flex-col gap-4 px-4 py-6">
        <div class="mb-1">
          <h1 class="text-2xl font-semibold tracking-tight">通过浏览器刷入固件</h1>
          <p class="mt-1 text-sm text-muted-foreground">基于 Web Serial，无需安装 ESP-IDF 或驱动。连接设备 → 刷入固件 → 查看日志。</p>
        </div>

        <Alert v-if="!f.supported" variant="destructive">
          <CircleAlert />
          <AlertTitle>当前浏览器不支持 Web Serial</AlertTitle>
          <AlertDescription>请使用桌面版 Chrome 或 Edge（89+），并通过 HTTPS 或 localhost 打开本页。</AlertDescription>
        </Alert>

        <div class="grid items-stretch gap-4 lg:grid-cols-2">
          <div class="flex min-w-0 flex-col gap-4">
            <ConnectCard />
            <FlashCard />
          </div>
          <MonitorCard />
        </div>
        <EsptoolLogCard />
      </main>

      <footer class="pb-8 text-center text-xs text-muted-foreground">
        <a class="underline-offset-4 hover:underline" href="https://github.com/foliageSea/Aemeath" target="_blank" rel="noopener">foliageSea/Aemeath</a>
        · 基于 <a class="underline-offset-4 hover:underline" href="https://github.com/espressif/esptool-js" target="_blank" rel="noopener">esptool-js</a>
        与 <a class="underline-offset-4 hover:underline" href="https://www.shadcn-vue.com" target="_blank" rel="noopener">shadcn-vue</a>
      </footer>
    </div>
  </TooltipProvider>
</template>
