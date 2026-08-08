/**
 * Admin Settings API endpoints
 * Handles system settings management for administrators
 */

import { apiClient } from '../client'
import type { CustomMenuItem } from '@/types'

export interface DefaultSubscriptionSetting {
  group_id: number
  validity_days: number
}

/**
 * System settings interface
 */
export interface SystemSettings {
  // Registration settings
  registration_enabled: boolean
  email_verify_enabled: boolean
  registration_email_suffix_whitelist: string[]
  promo_code_enabled: boolean
  password_reset_enabled: boolean
  frontend_url: string
  invitation_code_enabled: boolean
  totp_enabled: boolean // TOTP 双因素认证
  totp_encryption_key_configured: boolean // TOTP 加密密钥是否已配置
  // Default settings
  default_balance: number
  default_concurrency: number
  default_subscriptions: DefaultSubscriptionSetting[]
  // OEM settings
  site_name: string
  site_logo: string
  site_subtitle: string
  api_base_url: string
  contact_info: string
  doc_url: string
  home_content: string
  hide_ccs_import_button: boolean
  purchase_subscription_enabled: boolean
  purchase_subscription_url: string
  backend_mode_enabled: boolean
  custom_menu_items: CustomMenuItem[]
  // SMTP settings
  smtp_host: string
  smtp_port: number
  smtp_username: string
  smtp_password_configured: boolean
  smtp_from_email: string
  smtp_from_name: string
  smtp_use_tls: boolean
  // Cloudflare Turnstile settings
  turnstile_enabled: boolean
  turnstile_site_key: string
  turnstile_secret_key_configured: boolean

  // Model fallback configuration
  enable_model_fallback: boolean
  fallback_model_anthropic: string
  fallback_model_openai: string
  fallback_model_gemini: string

  // Identity patch configuration (Claude -> Gemini)
  enable_identity_patch: boolean
  identity_patch_prompt: string

  // Ops Monitoring (vNext)
  ops_monitoring_enabled: boolean
  ops_realtime_monitoring_enabled: boolean
  ops_query_mode_default: 'auto' | 'raw' | 'preagg' | string
  ops_metrics_interval_seconds: number

  // Claude Code version check
  min_claude_code_version: string
  max_claude_code_version: string

  // 分组隔离
  allow_ungrouped_key_scheduling: boolean
}

export interface UpdateSettingsRequest {
  registration_enabled?: boolean
  email_verify_enabled?: boolean
  registration_email_suffix_whitelist?: string[]
  promo_code_enabled?: boolean
  password_reset_enabled?: boolean
  frontend_url?: string
  invitation_code_enabled?: boolean
  totp_enabled?: boolean // TOTP 双因素认证
  default_balance?: number
  default_concurrency?: number
  default_subscriptions?: DefaultSubscriptionSetting[]
  site_name?: string
  site_logo?: string
  site_subtitle?: string
  api_base_url?: string
  contact_info?: string
  doc_url?: string
  home_content?: string
  hide_ccs_import_button?: boolean
  purchase_subscription_enabled?: boolean
  purchase_subscription_url?: string
  backend_mode_enabled?: boolean
  custom_menu_items?: CustomMenuItem[]
  smtp_host?: string
  smtp_port?: number
  smtp_username?: string
  smtp_password?: string
  smtp_from_email?: string
  smtp_from_name?: string
  smtp_use_tls?: boolean
  turnstile_enabled?: boolean
  turnstile_site_key?: string
  turnstile_secret_key?: string
  enable_model_fallback?: boolean
  fallback_model_anthropic?: string
  fallback_model_openai?: string
  fallback_model_gemini?: string
  enable_identity_patch?: boolean
  identity_patch_prompt?: string
  ops_monitoring_enabled?: boolean
  ops_realtime_monitoring_enabled?: boolean
  ops_query_mode_default?: 'auto' | 'raw' | 'preagg' | string
  ops_metrics_interval_seconds?: number
  min_claude_code_version?: string
  max_claude_code_version?: string
  allow_ungrouped_key_scheduling?: boolean
}

/**
 * Get all system settings
 * @returns System settings
 */
export async function getSettings(): Promise<SystemSettings> {
  const { data } = await apiClient.get<SystemSettings>('/admin/settings')
  return data
}

/**
 * Update system settings
 * @param settings - Partial settings to update
 * @returns Updated settings
 */
export async function updateSettings(settings: UpdateSettingsRequest): Promise<SystemSettings> {
  const { data } = await apiClient.put<SystemSettings>('/admin/settings', settings)
  return data
}

/**
 * Test SMTP connection request
 */
export interface TestSmtpRequest {
  smtp_host: string
  smtp_port: number
  smtp_username: string
  smtp_password: string
  smtp_use_tls: boolean
}

/**
 * Test SMTP connection with provided config
 * @param config - SMTP configuration to test
 * @returns Test result message
 */
export async function testSmtpConnection(config: TestSmtpRequest): Promise<{ message: string }> {
  const { data } = await apiClient.post<{ message: string }>('/admin/settings/test-smtp', config)
  return data
}

/**
 * Send test email request
 */
export interface SendTestEmailRequest {
  email: string
  smtp_host: string
  smtp_port: number
  smtp_username: string
  smtp_password: string
  smtp_from_email: string
  smtp_from_name: string
  smtp_use_tls: boolean
}

/**
 * Send test email with provided SMTP config
 * @param request - Email address and SMTP config
 * @returns Test result message
 */
export async function sendTestEmail(request: SendTestEmailRequest): Promise<{ message: string }> {
  const { data } = await apiClient.post<{ message: string }>(
    '/admin/settings/send-test-email',
    request
  )
  return data
}

/**
 * Admin API Key status response
 */
export interface AdminApiKeyStatus {
  exists: boolean
  masked_key: string
}

/**
 * Get admin API key status
 * @returns Status indicating if key exists and masked version
 */
export async function getAdminApiKey(): Promise<AdminApiKeyStatus> {
  const { data } = await apiClient.get<AdminApiKeyStatus>('/admin/settings/admin-api-key')
  return data
}

/**
 * Regenerate admin API key
 * @returns The new full API key (only shown once)
 */
export async function regenerateAdminApiKey(): Promise<{ key: string }> {
  const { data } = await apiClient.post<{ key: string }>('/admin/settings/admin-api-key/regenerate')
  return data
}

/**
 * Delete admin API key
 * @returns Success message
 */
export async function deleteAdminApiKey(): Promise<{ message: string }> {
  const { data } = await apiClient.delete<{ message: string }>('/admin/settings/admin-api-key')
  return data
}

// ==================== Overload Cooldown Settings ====================

/**
 * Overload cooldown settings interface (529 handling)
 */
export interface OverloadCooldownSettings {
  enabled: boolean
  cooldown_minutes: number
}

export async function getOverloadCooldownSettings(): Promise<OverloadCooldownSettings> {
  const { data } = await apiClient.get<OverloadCooldownSettings>('/admin/settings/overload-cooldown')
  return data
}

export async function updateOverloadCooldownSettings(
  settings: OverloadCooldownSettings
): Promise<OverloadCooldownSettings> {
  const { data } = await apiClient.put<OverloadCooldownSettings>(
    '/admin/settings/overload-cooldown',
    settings
  )
  return data
}

export const settingsAPI = {
  getSettings,
  updateSettings,
  testSmtpConnection,
  sendTestEmail,
  getAdminApiKey,
  regenerateAdminApiKey,
  deleteAdminApiKey,
  getOverloadCooldownSettings,
  updateOverloadCooldownSettings
}

export default settingsAPI
