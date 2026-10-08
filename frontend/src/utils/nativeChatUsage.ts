interface NativeChatUsage {
  billing_unit?: string
  model?: string
  inbound_endpoint?: string | null
  actual_cost?: number
  total_cost?: number
  output_cost?: number
  native_chat_turn_cost_usd?: number
  native_chat_image_cost_usd?: number
}

export function isNativeChatUsage(record: NativeChatUsage): boolean {
  return record.billing_unit === 'turn' || record.model === 'chatgpt-native-turn' ||
    record.inbound_endpoint === '/chatgpt/backend-api/f/conversation' ||
    record.inbound_endpoint === '/chatgpt/backend-api/f/conversation/resume'
}

export function nativeChatChargeCosts(record: NativeChatUsage): { turn: number; image: number } {
  const actual = record.actual_cost ?? 0
  const total = record.total_cost ?? 0
  const image = record.native_chat_image_cost_usd ?? (total > 0 ? (record.output_cost ?? 0) * actual / total : 0)
  return { turn: record.native_chat_turn_cost_usd ?? actual - image, image }
}
