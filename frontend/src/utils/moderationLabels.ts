// Display labels aligned with the versioned Jev category catalog. Unknown historical IDs remain visible.
const labels: Record<string, { zh: string; en: string }> = {
  "Jailbreak": {
    "zh": "越狱与指令绕过",
    "en": "Jailbreak"
  },
  "PII": {
    "zh": "隐私滥用",
    "en": "Privacy abuse"
  },
  "Non-violent Illegal Acts": {
    "zh": "非暴力违法",
    "en": "Non-violent crime"
  },
  "Unethical Acts": {
    "zh": "恶意欺骗与不道德行为",
    "en": "Harmful deception and abuse"
  },
  "harassment": {
    "zh": "定向骚扰",
    "en": "Targeted harassment"
  },
  "harassment/threatening": {
    "zh": "骚扰中的暴力威胁",
    "en": "Threatening harassment"
  },
  "hate": {
    "zh": "身份仇恨",
    "en": "Identity-based hate"
  },
  "hate/threatening": {
    "zh": "仇恨中的暴力煽动",
    "en": "Threatening hate"
  },
  "illicit/violent": {
    "zh": "暴力违法协助",
    "en": "Violent wrongdoing assistance"
  },
  "violence": {
    "zh": "暴力描写或鼓励",
    "en": "Violence"
  },
  "violence/graphic": {
    "zh": "血腥暴力",
    "en": "Graphic violence"
  },
  "sexual": {
    "zh": "成人露骨色情",
    "en": "Explicit sexual content"
  },
  "sexual/minors": {
    "zh": "未成年人性化",
    "en": "Sexualization of minors"
  },
  "sexual/non-consensual": {
    "zh": "非自愿性剥削",
    "en": "Non-consensual sexual exploitation"
  },
  "self-harm/intent": {
    "zh": "自伤意图（仅记录）",
    "en": "Self-harm intent (audit only)"
  },
  "self-harm/encouragement": {
    "zh": "鼓励自伤",
    "en": "Encouraging self-harm"
  },
  "self-harm/instructions": {
    "zh": "自伤方法协助",
    "en": "Self-harm instructions"
  },
  "political/manipulation": {
    "zh": "政治场景中的有害行为",
    "en": "Harmful political conduct"
  },
  "copyright/unauthorized": {
    "zh": "明确的版权滥用风险",
    "en": "Explicit unauthorized copyright use"
  }
}

export function moderationCategoryLabel(category: string, locale: string): string {
  const label = labels[category]
  return label ? (locale.startsWith('zh') ? label.zh : label.en) : category
}
