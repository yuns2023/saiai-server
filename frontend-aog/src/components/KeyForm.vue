<script setup lang="ts">
/** Create or edit a key. Limits are tucked behind "More limits". */
import { computed, reactive, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import type { ApiKey, Group } from '@/types'
import { rate } from '@/lib/format'

export interface KeyFormValue {
  name: string
  group_id: number | null
  quota: number | null
  expires_in_days: number | null
  max_concurrency: number | null
  rate_limit_5h: number | null
  rate_limit_1d: number | null
  rate_limit_7d: number | null
  ip_whitelist: string
  ip_blacklist: string
}

const props = defineProps<{ groups: Group[]; rates: Record<number, number>; initial?: ApiKey | null; busy?: boolean }>()
const emit = defineEmits<{ submit: [value: KeyFormValue]; cancel: [] }>()
const { t } = useI18n()

const k = props.initial
const form = reactive<KeyFormValue>({
  name: k?.name ?? '',
  group_id: k?.group_id ?? props.groups[0]?.id ?? null,
  quota: k?.quota ? k.quota : null,
  expires_in_days: null,
  max_concurrency: k?.max_concurrency ?? null,
  rate_limit_5h: k?.rate_limit_5h || null,
  rate_limit_1d: k?.rate_limit_1d || null,
  rate_limit_7d: k?.rate_limit_7d || null,
  ip_whitelist: (k?.ip_whitelist ?? []).join('\n'),
  ip_blacklist: (k?.ip_blacklist ?? []).join('\n')
})
const more = ref(!!(k && (k.rate_limit_5h || k.rate_limit_1d || k.rate_limit_7d || k.ip_whitelist?.length || k.ip_blacklist?.length)))

const platformLabel: Record<string, string> = { anthropic: 'Anthropic', openai: 'OpenAI', gemini: 'Google', antigravity: 'Google', sora: 'OpenAI' }
const effectiveRate = (g: Group) => props.rates[g.id] ?? g.rate_multiplier
/**
 * v-model.number yields "" for a cleared input and the raw string for
 * unparsable text, so every numeric field is normalised here: empty → null,
 * otherwise a finite number that passes the field's own check.
 */
type NumRule = { int?: boolean; min: number }
function readNum(raw: unknown, rule: NumRule): { ok: boolean; value: number | null } {
  if (raw === '' || raw === null || raw === undefined) return { ok: true, value: null }
  const n = typeof raw === 'number' ? raw : Number(raw)
  if (!Number.isFinite(n) || n < rule.min || (rule.int && !Number.isInteger(n))) return { ok: false, value: null }
  return { ok: true, value: n }
}
const rules: Record<'quota' | 'expires_in_days' | 'max_concurrency' | 'rate_limit_5h' | 'rate_limit_1d' | 'rate_limit_7d', NumRule> = {
  quota: { min: 0 },
  expires_in_days: { int: true, min: 0 },
  max_concurrency: { int: true, min: 1 },
  rate_limit_5h: { min: 0 },
  rate_limit_1d: { min: 0 },
  rate_limit_7d: { min: 0 }
}
const badFields = computed(() => (Object.keys(rules) as Array<keyof typeof rules>).filter((k) => !readNum(form[k], rules[k]).ok))
const valid = computed(() => form.name.trim().length > 0 && form.name.trim().length <= 100 && badFields.value.length === 0)

const formEl = ref<HTMLFormElement | null>(null)
const badInput = ref(false)

function submit(): void {
  // A half-typed number ("1e") reports value "" with validity.badInput; treat it as invalid, not as cleared.
  badInput.value = !!formEl.value && Array.from(formEl.value.querySelectorAll<HTMLInputElement>('input[type=number]')).some((i) => i.validity.badInput)
  if (badInput.value || !valid.value || props.busy) return
  const out: KeyFormValue = { ...form, name: form.name.trim() }
  for (const k of Object.keys(rules) as Array<keyof typeof rules>) out[k] = readNum(form[k], rules[k]).value
  emit('submit', out)
}
</script>

<template>
  <form ref="formEl" class="space-y-4" novalidate @submit.prevent="submit">
    <div class="grid gap-4 sm:grid-cols-2">
      <label class="block">
        <span class="label">{{ t('keys.name') }}</span>
        <input v-model="form.name" class="field mt-1" maxlength="100" required autofocus :placeholder="t('keys.namePlaceholder')" />
      </label>
      <label class="block">
        <span class="label">{{ t('keys.group') }}</span>
        <select v-model="form.group_id" class="field mt-1">
          <option v-for="g in groups" :key="g.id" :value="g.id">
            {{ g.name }} · {{ platformLabel[g.platform] ?? g.platform }} · {{ rate(effectiveRate(g)) }}
          </option>
        </select>
      </label>
      <label class="block">
        <span class="label">{{ t('keys.quota') }} <span class="text-ink-3">· {{ t('keys.quotaHint') }}</span></span>
        <input v-model.number="form.quota" :aria-invalid="badFields.includes('quota') ? 'true' : undefined" class="field num mt-1" type="number" min="0" step="0.01" inputmode="decimal" placeholder="0" />
      </label>
      <label v-if="!initial" class="block">
        <span class="label">{{ t('keys.expiresIn') }} <span class="text-ink-3">· {{ t('keys.expiresHint') }}</span></span>
        <input v-model.number="form.expires_in_days" :aria-invalid="badFields.includes('expires_in_days') ? 'true' : undefined" class="field num mt-1" type="number" min="0" step="1" inputmode="numeric" placeholder="0" />
      </label>
      <label class="block">
        <span class="label">{{ t('keys.concurrency') }}</span>
        <input v-model.number="form.max_concurrency" :aria-invalid="badFields.includes('max_concurrency') ? 'true' : undefined" class="field num mt-1" type="number" min="1" step="1" inputmode="numeric" :placeholder="t('keys.concurrencyDefault')" />
      </label>
    </div>

    <button type="button" class="btn btn-quiet btn-sm -ml-2" :aria-expanded="more" @click="more = !more">
      {{ more ? t('keys.fewerLimits') : t('keys.moreLimits') }}
    </button>
    <div v-if="more" class="grid gap-4 sm:grid-cols-3">
      <label class="block">
        <span class="label">{{ t('keys.limit5h') }}</span>
        <input v-model.number="form.rate_limit_5h" :aria-invalid="badFields.includes('rate_limit_5h') ? 'true' : undefined" class="field num mt-1" type="number" min="0" step="0.01" placeholder="0" />
      </label>
      <label class="block">
        <span class="label">{{ t('keys.limit1d') }}</span>
        <input v-model.number="form.rate_limit_1d" :aria-invalid="badFields.includes('rate_limit_1d') ? 'true' : undefined" class="field num mt-1" type="number" min="0" step="0.01" placeholder="0" />
      </label>
      <label class="block">
        <span class="label">{{ t('keys.limit7d') }}</span>
        <input v-model.number="form.rate_limit_7d" :aria-invalid="badFields.includes('rate_limit_7d') ? 'true' : undefined" class="field num mt-1" type="number" min="0" step="0.01" placeholder="0" />
      </label>
      <label class="block sm:col-span-3 md:col-span-1">
        <span class="label">{{ t('keys.ipAllow') }}</span>
        <textarea v-model="form.ip_whitelist" class="field mt-1 h-20 py-2 font-mono text-xs" :placeholder="t('keys.ipHint')" />
      </label>
      <label class="block sm:col-span-3 md:col-span-1">
        <span class="label">{{ t('keys.ipDeny') }}</span>
        <textarea v-model="form.ip_blacklist" class="field mt-1 h-20 py-2 font-mono text-xs" :placeholder="t('keys.ipHint')" />
      </label>
    </div>

    <p v-if="badFields.length || badInput" class="text-sm text-seal" role="alert">{{ t('keys.invalidNumbers') }}</p>
    <div class="flex gap-2 pt-1">
      <button type="submit" class="btn btn-primary" :disabled="busy || !valid">{{ initial ? t('common.save') : t('keys.create') }}</button>
      <button type="button" class="btn btn-line" :disabled="busy" @click="emit('cancel')">{{ t('common.cancel') }}</button>
    </div>
  </form>
</template>
