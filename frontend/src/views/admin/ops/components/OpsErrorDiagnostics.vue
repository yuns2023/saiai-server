<template>
  <section class="rounded-xl bg-gray-50 p-6 dark:bg-dark-900">
    <h3 class="text-sm font-bold text-gray-900 dark:text-white">{{ label('title') }}</h3>
    <div class="mt-4 overflow-x-auto">
      <table class="w-full text-left text-sm">
        <thead class="text-gray-500"><tr><th class="p-2">{{ label('field') }}</th><th class="p-2">{{ label('value') }}</th></tr></thead>
        <tbody class="divide-y divide-gray-200 dark:divide-dark-700">
          <tr v-for="row in facts" :key="row.field"><th class="p-2 font-medium text-gray-500">{{ row.field }}</th><td class="break-all p-2 font-mono text-gray-900 dark:text-gray-100">{{ row.value }}</td></tr>
        </tbody>
      </table>
    </div>
    <h4 class="mt-6 text-sm font-bold text-gray-900 dark:text-white">{{ label('checks') }}</h4>
    <p v-if="!checks.length" class="mt-2 text-sm text-gray-500">{{ label('unknownChecks') }}</p>
    <div v-else class="mt-3 overflow-x-auto">
      <table class="w-full text-left text-sm" data-testid="diagnostic-checks">
        <thead class="text-gray-500"><tr><th v-for="column in ['field', 'expected', 'actual', 'result']" :key="column" class="p-2">{{ label(column) }}</th></tr></thead>
        <tbody class="divide-y divide-gray-200 dark:divide-dark-700">
          <tr v-for="check in checks" :key="check.field">
            <th class="p-2 font-mono font-medium">{{ check.field }}</th>
            <td class="p-2">{{ label(check.expected) }}</td><td class="p-2">{{ label(check.actual) }}</td>
            <td class="p-2 font-bold" :class="check.passed ? 'text-emerald-600' : 'text-red-600'">{{ label(check.passed ? 'passed' : 'failed') }}</td>
          </tr>
        </tbody>
      </table>
    </div>
    <p class="mt-3 text-xs text-gray-500">{{ label('privacyHint') }}</p>
  </section>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import type { OpsErrorDetail } from '@/api/admin/ops'

const props = defineProps<{ detail: OpsErrorDetail }>()
const { t } = useI18n()
const label = (key: string) => t(`admin.ops.errorDetail.diagnostics.${key}`)
const checks = computed(() => props.detail.diagnostics?.schema_version === 1 ? props.detail.diagnostics.checks ?? [] : [])
const facts = computed(() => {
  const d = props.detail
  let body: Record<string, unknown> = {}
  try {
    const parsed: unknown = JSON.parse(d.error_body || '{}')
    if (parsed && typeof parsed === 'object' && !Array.isArray(parsed)) body = parsed as Record<string, unknown>
  } catch { /* Historical errors may contain plain text. */ }
  const scalar = (key: string) => typeof body[key] === 'number' || typeof body[key] === 'string' ? String(body[key]).slice(0, 256) : undefined
  const error = body.error && typeof body.error === 'object' && !Array.isArray(body.error) ? body.error as Record<string, unknown> : {}
  const errorField = (key: string) => typeof error[key] === 'string' ? String(error[key]).slice(0, 256) : undefined
  const entries: [string, unknown][] = [
    [label('phase'), d.phase], [label('source'), d.error_source], [label('owner'), d.error_owner],
    ['request_id', d.request_id], ['client_request_id', d.client_request_id],
    [label('forwardedHost'), d.diagnostics?.schema_version === 1 ? d.diagnostics.forwarded_host : undefined],
    [label('requestHost'), d.diagnostics?.schema_version === 1 ? d.diagnostics.request_host : undefined],
    [label('transport'), d.diagnostics?.transport],
    ['status_code', d.status_code],
    ['error.type', errorField('type')], ['error.code', errorField('code')], ['error.param', errorField('param')],
    ['transport_status_code', d.diagnostics?.transport_status_code || scalar('transport_status_code')],
    ['upstream_status_code', d.upstream_status_code ?? scalar('upstream_status_code')],
    ['client_policy', d.diagnostics?.client_policy], ['version', d.diagnostics?.client_version],
    ['User-Agent', d.user_agent], ['model', d.model],
    ['websocket_close_code', scalar('websocket_close_code')], ['websocket_turn', scalar('websocket_turn')],
    ['websocket_event_type', scalar('websocket_event_type')], ['upstream_request_id', scalar('upstream_request_id')],
    ['request_payload_sha256', /^[a-f0-9]{64}$/i.test(String(body.request_payload_sha256 ?? '')) ? body.request_payload_sha256 : undefined],
    ['request_body_bytes', d.request_body_bytes],
    [label('bodyCapture'), d.request_body ? label(d.request_body_truncated ? 'truncated' : 'captured') : label('notCaptured')],
    ['auth_latency_ms', d.auth_latency_ms], ['routing_latency_ms', d.routing_latency_ms],
    ['upstream_latency_ms', d.upstream_latency_ms], ['response_latency_ms', d.response_latency_ms],
    ['time_to_first_token_ms', d.time_to_first_token_ms]
  ]
  return entries.filter(([, value]) => value != null && value !== '').map(([field, value]) => ({ field, value: String(value) }))
})
</script>
