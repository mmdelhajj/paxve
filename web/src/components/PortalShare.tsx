import { useState } from 'react';
import { Button, CopyButton, Group, Modal, Stack, Text, TextInput } from '@mantine/core';
import { IconBrandWhatsapp, IconCopy, IconLink, IconRefresh } from '@tabler/icons-react';
import { useTranslation } from 'react-i18next';
import { api } from '../api';
import { useCompany } from '../company';
import { notifyError } from '../util';
import { Confirm } from './PageHeader';

/** Shows the customer's secret portal link: copy, open in WhatsApp, or regenerate (old link stops working). */
export default function PortalShare({ customerId, phone, canRegenerate }: { customerId: string; phone?: string; canRegenerate: boolean }) {
  const { t } = useTranslation();
  const { company } = useCompany();
  const [url, setUrl] = useState<string | null>(null);
  const [confirm, setConfirm] = useState(false);
  const [busy, setBusy] = useState(false);

  async function open(regenerate = false) {
    if (!company) return;
    setBusy(true);
    try {
      const r = await api<{ url: string }>(`/companies/${company.id}/customers/${customerId}/portal`,
        regenerate ? { method: 'POST', body: {} } : {});
      setUrl(r.url);
      setConfirm(false);
    } catch (e) {
      notifyError(t, e);
    } finally {
      setBusy(false);
    }
  }

  const digits = (phone ?? '').replace(/\D/g, '');
  const wa = url ? `https://wa.me/${digits.startsWith('961') || digits.length > 9 ? digits : digits ? '961' + digits.replace(/^0/, '') : ''}?text=${encodeURIComponent(t('portal.shareText') + ' ' + url)}` : '';

  return (
    <>
      <Button variant="default" leftSection={<IconLink size={16} />} loading={busy && !url} onClick={() => open()}>{t('portal.share')}</Button>
      {url && (
        <Modal opened onClose={() => setUrl(null)} title={t('portal.share')}>
          <Stack>
            <Text size="sm" c="dimmed">{t('portal.shareHint')}</Text>
            <TextInput value={url} readOnly dir="ltr" onFocus={(e) => e.currentTarget.select()} />
            <Group grow>
              <CopyButton value={url}>
                {({ copied, copy }) => (
                  <Button leftSection={<IconCopy size={16} />} color={copied ? 'teal' : undefined} onClick={copy}>
                    {copied ? t('dev.copied') : t('dev.copy')}
                  </Button>
                )}
              </CopyButton>
              <Button component="a" href={wa} target="_blank" rel="noopener" color="green" leftSection={<IconBrandWhatsapp size={16} />}>WhatsApp</Button>
            </Group>
            {canRegenerate && (confirm ? (
              <Confirm text={t('portal.regenConfirm')} yesLabel={t('portal.regen')} noLabel={t('common.cancel')} color="orange"
                busy={busy} onYes={() => open(true)} onNo={() => setConfirm(false)} />
            ) : (
              <Button variant="subtle" color="orange" size="xs" leftSection={<IconRefresh size={14} />} onClick={() => setConfirm(true)}>{t('portal.regen')}</Button>
            ))}
          </Stack>
        </Modal>
      )}
    </>
  );
}
