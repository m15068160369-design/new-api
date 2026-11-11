/*
Copyright (C) 2023-2026 QuantumNous

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as
published by the Free Software Foundation, either version 3 of the
License, or (at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
GNU Affero General Public License for more details.

You should have received a copy of the GNU Affero General Public License
along with this program. If not, see <https://www.gnu.org/licenses/>.

For commercial licensing, please contact support@quantumnous.com
*/
import { api } from '@/lib/api'

// Catalog / pair / save admin endpoints. Match
// controller/topup_waffo_pancake.go: empty body creds make the backend
// fall back to persisted OptionMap values, so returning admins don't
// have to re-paste the private key (stripped from GET /api/option/).
//
// The CN/US split reuses these same endpoints, scoped by `channel`. Each
// channel is an independent gateway with its own credentials, product and
// pricing, so catalog/pair/save all have to be told which one they mean.

export type WaffoPancakeChannel = 'cn' | 'us'

export interface CatalogProduct {
  id: string
  name: string
  status: string
}

export interface CatalogStore {
  id: string
  name: string
  status: string
  prodEnabled: boolean
  onetimeProducts: CatalogProduct[]
}

export interface PairResult {
  store_id: string
  store_name: string
  product_id: string
  product_name: string
}

export interface PairOrphanError {
  error?: string
  orphan_store?: boolean
  store_id?: string
  store_name?: string
}

interface BackendBody<T> {
  message?: string
  data?: T | string
}

export type CatalogResponse = BackendBody<{ stores: CatalogStore[] }>
export type PairResponse = BackendBody<PairResult>
export type SaveResponse = BackendBody<{ product_id: string; store_id: string }>

export async function listWaffoPancakeCatalog(
  merchantID: string,
  privateKey: string,
  channel?: WaffoPancakeChannel
): Promise<CatalogResponse> {
  const res = await api.get<CatalogResponse>(
    '/api/option/waffo-pancake/catalog',
    {
      params: {
        merchant_id: merchantID,
        private_key: privateKey,
        ...(channel ? { channel } : {}),
      },
    }
  )
  return res.data
}

export async function createWaffoPancakePair(params: {
  merchantID: string
  privateKey: string
  returnURL: string
  channel?: WaffoPancakeChannel
}): Promise<PairResponse> {
  const res = await api.post<PairResponse>('/api/option/waffo-pancake/pair', {
    merchant_id: params.merchantID,
    private_key: params.privateKey,
    return_url: params.returnURL,
    ...(params.channel ? { channel: params.channel } : {}),
  })
  return res.data
}

export async function saveWaffoPancakeConfig(params: {
  merchantID: string
  privateKey: string
  returnURL: string
  storeID: string
  productID: string
  /** Omitted for the legacy single-instance gateway. */
  channel?: WaffoPancakeChannel
  /** Channel-scoped pricing; omitted for the legacy gateway. */
  unitPrice?: number
  ratio?: number
}): Promise<SaveResponse> {
  const res = await api.post<SaveResponse>('/api/option/waffo-pancake/save', {
    merchant_id: params.merchantID,
    private_key: params.privateKey,
    return_url: params.returnURL,
    store_id: params.storeID,
    product_id: params.productID,
    ...(params.channel ? { channel: params.channel } : {}),
    ...(params.unitPrice !== undefined
      ? { unit_price: params.unitPrice }
      : {}),
    ...(params.ratio !== undefined ? { ratio: params.ratio } : {}),
  })
  return res.data
}
