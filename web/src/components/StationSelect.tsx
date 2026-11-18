import { useEffect, useState } from 'react';
import { Select } from '@mantine/core';
import { useTranslation } from 'react-i18next';
import { api } from '../api';
import { useCompany } from '../company';

/** Station filter (generator site). Renders nothing when the company has no stations. */
export default function StationSelect({ value, onChange, label, size = 'sm', w = 180 }: {
  value: string | null; onChange: (v: string | null) => void; label?: string; size?: 'xs' | 'sm'; w?: number;
}) {
  const { t } = useTranslation();
  const { company } = useCompany();
  const [stations, setStations] = useState<{ id: string; name: string }[]>([]);
  useEffect(() => {
    if (company) api<{ id: string; name: string }[]>(`/companies/${company.id}/stations`).then(setStations).catch(() => setStations([]));
  }, [company]);
  if (stations.length === 0) return null;
  return (
    <Select size={size} w={w} label={label} placeholder={t('net.allStations')} clearable value={value} onChange={onChange}
      data={stations.map((s) => ({ value: s.id, label: s.name }))} />
  );
}
