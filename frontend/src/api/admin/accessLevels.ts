import { apiClient } from '../client'
import type { AccessLevel } from '@/types'

export interface AccessLevelInput {
  name: string
  rank: number
  balance_threshold: number
  payg_discount_multiplier: number
}

export async function list(): Promise<AccessLevel[]> {
  const { data } = await apiClient.get<AccessLevel[]>('/admin/access-levels')
  return data
}

export async function create(input: AccessLevelInput): Promise<AccessLevel> {
  const { data } = await apiClient.post<AccessLevel>('/admin/access-levels', input)
  return data
}

export async function update(id: number, input: AccessLevelInput): Promise<AccessLevel> {
  const { data } = await apiClient.put<AccessLevel>(`/admin/access-levels/${id}`, input)
  return data
}

export async function remove(id: number): Promise<void> {
  await apiClient.delete(`/admin/access-levels/${id}`)
}

export async function getSettings(): Promise<{ balance_maintenance_enabled: boolean }> {
  const { data } = await apiClient.get<{ balance_maintenance_enabled: boolean }>('/admin/access-levels/settings')
  return data
}

export async function updateSettings(enabled: boolean): Promise<{ balance_maintenance_enabled: boolean }> {
  const { data } = await apiClient.put<{ balance_maintenance_enabled: boolean }>('/admin/access-levels/settings', {
    balance_maintenance_enabled: enabled
  })
  return data
}

export async function setUserManualLevel(userId: number, levelId: number): Promise<void> {
  await apiClient.put(`/admin/users/${userId}/access-level`, { level_id: levelId })
}

export async function clearUserManualLevel(userId: number): Promise<void> {
  await apiClient.delete(`/admin/users/${userId}/access-level`)
}

export default { list, create, update, remove, getSettings, updateSettings, setUserManualLevel, clearUserManualLevel }
