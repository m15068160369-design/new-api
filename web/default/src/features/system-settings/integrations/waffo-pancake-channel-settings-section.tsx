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
import * as React from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { api } from '@/lib/api'

import { saveWaffoPancakeChannelConfig } from './waffo-pancake-api'

export type WaffoPancakeChannel = 'cn' | 'us'

const CHANNEL_META: Record<
  WaffoPancakeChannel,
  { prefix: string }
> = {
  cn: { prefix: 'WaffoPancakeCN' },
  us: { prefix: 'WaffoPancakeUS' },
}

interface OptionEntry {
  key: string
  value: string
}

/**
 * Waffo Pancake dual-channel settings. Each channel (CN priced in CNY via
 * WeChat Pay, US in USD via cards) is a fully independent gateway: own
 * credentials, own product, own unit price x channel ratio pricing and own
 * minimum top-up. The section loads its own persisted values from
 * /api/option/ (the private key is stripped server-side) and saves through
 * /api/option/waffo-pancake-channel/save.
 */
export function WaffoPancakeChannelSettingsSection({
  channel,
}: {
  channel: WaffoPancakeChannel
}) {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const prefix = CHANNEL_META[channel].prefix

  const { data: rawOptions } = useQuery({
    queryKey: ['system-options'],
    queryFn: async () => {
      const res = await api.get('/api/option/')
      return res.data as { success: boolean; data?: OptionEntry[] }
    },
  })

  const persisted = React.useMemo(() => {
    const map: Record<string, string> = {}
    for (const entry of rawOptions?.data ?? []) {
      map[entry.key] = entry.value
    }
    return map
  }, [rawOptions])

  const [merchantID, setMerchantID] = React.useState('')
  const [privateKey, setPrivateKey] = React.useState('')
  const [productID, setProductID] = React.useState('')
  const [unitPrice, setUnitPrice] = React.useState('1')
  const [ratio, setRatio] = React.useState('1')
  const [minTopUp, setMinTopUp] = React.useState('1')
  const [saving, setSaving] = React.useState(false)

  // Sync persisted values once per load; later edits stay local until saved.
  const loadedRef = React.useRef(false)
  React.useEffect(() => {
    if (loadedRef.current || !rawOptions?.data) {
      return
    }
    loadedRef.current = true
    setMerchantID(persisted[`${prefix}MerchantID`] ?? '')
    setProductID(persisted[`${prefix}ProductID`] ?? '')
    setUnitPrice(persisted[`${prefix}UnitPrice`] ?? '1')
    setRatio(persisted[`${prefix}Ratio`] ?? '1')
    setMinTopUp(persisted[`${prefix}MinTopUp`] ?? '1')
  }, [rawOptions, persisted, prefix])

  const configured = merchantID.trim() !== '' && productID.trim() !== ''

  const handleSave = async () => {
    setSaving(true)
    try {
      const body = await saveWaffoPancakeChannelConfig({
        channel,
        merchantID: merchantID.trim(),
        privateKey: privateKey.trim(),
        productID: productID.trim(),
        unitPrice: Number.parseFloat(unitPrice) || 0,
        ratio: Number.parseFloat(ratio) || 1,
        minTopUp: Number.parseInt(minTopUp, 10) || 1,
      })
      if (body?.message === 'success') {
        await queryClient.invalidateQueries({ queryKey: ['system-options'] })
        toast.success(t('Waffo Pancake channel settings saved'))
        setPrivateKey('')
        return
      }
      toast.error(t('Failed to save Waffo Pancake channel settings'))
    } catch (_error) {
      toast.error(t('Failed to save Waffo Pancake channel settings'))
    } finally {
      setSaving(false)
    }
  }

  return (
    <div className='flex flex-col gap-4'>
      <div className='text-muted-foreground text-sm'>
        {channel === 'cn'
          ? t(
              'CNY-priced channel, paid via WeChat Pay only (WeChat caps each buyer at ~¥1,000 per calendar month). Fully independent credentials, product, unit price, channel ratio and minimum top-up. Pricing: top-up amount × unit price × group ratio × discount × channel ratio.'
            )
          : t(
              'USD-priced channel, paid by card / Apple Pay / Google Pay. Fully independent credentials, product, unit price, channel ratio and minimum top-up. Pricing: top-up amount × unit price × group ratio × discount × channel ratio.'
            )}
      </div>

      <div className='grid gap-4'>
        <div className='grid gap-1.5'>
          <Label>{t('Merchant ID')}</Label>
          <Input
            value={merchantID}
            onChange={(e) => setMerchantID(e.target.value)}
            placeholder='MER_...'
          />
        </div>

        <div className='grid gap-1.5'>
          <Label>{t('API Private Key')}</Label>
          <Input
            type='password'
            value={privateKey}
            onChange={(e) => setPrivateKey(e.target.value)}
            placeholder={
              merchantID
                ? t('Leave blank to keep the saved key')
                : t('Paste the Pancake private key')
            }
          />
        </div>

        <div className='grid gap-1.5'>
          <Label>{t('Product ID')}</Label>
          <Input
            value={productID}
            onChange={(e) => setProductID(e.target.value)}
            placeholder={
              channel === 'cn'
                ? t('PROD_... (your CNY-priced product)')
                : t('PROD_... (your USD-priced product)')
            }
          />
          <p className='text-muted-foreground text-xs'>
            {t(
              'Create the product in the Pancake dashboard first, then paste its ID here. Blank disables this channel.'
            )}
          </p>
        </div>

        <div className='grid grid-cols-1 gap-4 sm:grid-cols-3'>
          <div className='grid gap-1.5'>
            <Label>{t('Unit price')}</Label>
            <Input
              type='number'
              min='0'
              step='0.01'
              value={unitPrice}
              onChange={(e) => setUnitPrice(e.target.value)}
            />
            <p className='text-muted-foreground text-xs'>
              {channel === 'cn'
                ? t('Price per 1 unit, in ¥')
                : t('Price per 1 unit, in $')}
            </p>
          </div>
          <div className='grid gap-1.5'>
            <Label>{t('Channel ratio')}</Label>
            <Input
              type='number'
              min='0'
              step='0.01'
              value={ratio}
              onChange={(e) => setRatio(e.target.value)}
            />
            <p className='text-muted-foreground text-xs'>
              {t('Extra multiplier, e.g. 0.9 for a 10% off promo')}
            </p>
          </div>
          <div className='grid gap-1.5'>
            <Label>{t('Minimum top-up')}</Label>
            <Input
              type='number'
              min='1'
              step='1'
              value={minTopUp}
              onChange={(e) => setMinTopUp(e.target.value)}
            />
            <p className='text-muted-foreground text-xs'>
              {t('Minimum units per top-up on this channel')}
            </p>
          </div>
        </div>
      </div>

      <div className='flex items-center gap-3'>
        <Button onClick={handleSave} disabled={saving}>
          {saving ? t('Saving...') : t('Save channel settings')}
        </Button>
        <span
          className={
            configured
              ? 'text-xs text-green-600'
              : 'text-muted-foreground text-xs'
          }
        >
          {configured
            ? t('Configured — shows in the wallet top-up page')
            : t('Not configured — channel hidden from users')}
        </span>
      </div>
    </div>
  )
}
