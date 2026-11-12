<template>
  <AppLayout>
    <div class="card overflow-hidden">
      <iframe
        :src="embeddedUrl"
        :title="t('nav.accountEvaluation')"
        class="block w-full border-0"
        style="height: calc(100dvh - 160px); min-height: 600px"
      ></iframe>
    </div>
  </AppLayout>
</template>

<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import AppLayout from '@/components/layout/AppLayout.vue'
import { buildEmbeddedUrl, detectTheme } from '@/utils/embedded-url'

const { t, locale } = useI18n()
const pageTheme = ref<'light' | 'dark'>(detectTheme())
// Resolve in the browser so a reverse proxy cannot discard the external port.
const embeddedUrl = computed(() => buildEmbeddedUrl('/codex-eval', undefined, undefined, pageTheme.value, locale.value))
let themeObserver: MutationObserver | null = null
onMounted(() => {
  themeObserver = new MutationObserver(() => { pageTheme.value = detectTheme() })
  themeObserver.observe(document.documentElement, { attributes: true, attributeFilter: ['class'] })
})
onUnmounted(() => { themeObserver?.disconnect() })
</script>
