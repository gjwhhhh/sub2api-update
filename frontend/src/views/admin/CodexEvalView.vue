<template>
  <AppLayout>
    <div class="card overflow-hidden">
      <iframe
        ref="evaluationFrame"
        :src="embeddedUrl"
        :title="t('nav.accountEvaluation')"
        class="block w-full border-0"
        style="height: calc(100dvh - 160px); min-height: 600px"
      ></iframe>
    </div>
    <TotpStepUpDialog :controller="stepUp" />
  </AppLayout>
</template>

<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import AppLayout from '@/components/layout/AppLayout.vue'
import TotpStepUpDialog from '@/components/auth/TotpStepUpDialog.vue'
import { useStepUp } from '@/composables/useStepUp'
import { buildEmbeddedUrl, detectTheme } from '@/utils/embedded-url'

const { t, locale } = useI18n()
const stepUp = useStepUp()
const evaluationFrame = ref<HTMLIFrameElement | null>(null)
async function onStepUp(event: MessageEvent) {
  const frame = evaluationFrame.value?.contentWindow
  if (!frame || event.origin !== window.location.origin || event.source !== frame || event.data?.type !== 'evaluation.step-up') return
  const requestId = event.data.requestId
  if (typeof requestId !== 'string') return
  const ok = await stepUp.prompt()
  frame.postMessage({ type: 'evaluation.step-up.result', requestId, ok }, window.location.origin)
}
const pageTheme = ref<'light' | 'dark'>(detectTheme())
// Resolve in the browser so a reverse proxy cannot discard the external port.
const embeddedUrl = computed(() => buildEmbeddedUrl('/codex-eval', undefined, undefined, pageTheme.value, locale.value))
let themeObserver: MutationObserver | null = null
onMounted(() => {
  window.addEventListener('message', onStepUp)
  themeObserver = new MutationObserver(() => { pageTheme.value = detectTheme() })
  themeObserver.observe(document.documentElement, { attributes: true, attributeFilter: ['class'] })
})
onUnmounted(() => { window.removeEventListener('message', onStepUp); stepUp.onCancel(); themeObserver?.disconnect() })
</script>
