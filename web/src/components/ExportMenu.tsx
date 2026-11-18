import { useState } from 'react';
import { Button, Menu } from '@mantine/core';
import { IconDownload, IconFileSpreadsheet, IconFileText, IconPrinter } from '@tabler/icons-react';
import { useTranslation } from 'react-i18next';
import { API_BASE, ApiError } from '../api';
import { printPage, saveFile } from '../native';
import { notifyError } from '../util';

export type Cell = string | number | null | undefined;

export interface ExportTable {
  title: string;
  subtitle?: string;
  columns: { label: string; numeric?: boolean }[];
  rows: Cell[][];
  footer?: Cell[][];
}

function fileName(title: string, ext: string): string {
  const safe = title.replace(/[^\p{L}\p{N}_.-]+/gu, '-').replace(/^-+|-+$/g, '') || 'paxve';
  return `${safe}-${new Date().toISOString().slice(0, 10)}.${ext}`;
}

function csvCell(v: Cell): string {
  if (v === null || v === undefined) return '';
  const s = String(v);
  return /[",\n\r;]/.test(s) ? `"${s.replace(/"/g, '""')}"` : s;
}

/** CSV text (UTF-8 with BOM so Excel opens Arabic correctly). */
export function toCSV(t: ExportTable): string {
  const lines = [t.columns.map((c) => csvCell(c.label)).join(',')];
  for (const r of [...t.rows, ...(t.footer ?? [])]) lines.push(r.map(csvCell).join(','));
  return '﻿' + lines.join('\r\n') + '\r\n';
}

/** Export menu for report pages: CSV, Excel (.xlsx built by the server) and Print / PDF. */
export default function ExportMenu({ table, disabled }: { table: () => ExportTable | null; disabled?: boolean }) {
  const { t, i18n } = useTranslation();
  const [busy, setBusy] = useState(false);

  async function csv() {
    const tb = table();
    if (!tb) return;
    try {
      await saveFile(fileName(tb.title, 'csv'), toCSV(tb), 'text/csv;charset=utf-8');
    } catch (e) {
      notifyError(t, e);
    }
  }

  async function xlsx() {
    const tb = table();
    if (!tb) return;
    setBusy(true);
    try {
      const res = await fetch(API_BASE + '/export/xlsx', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json', 'X-Requested-With': 'paxve' },
        credentials: 'same-origin',
        body: JSON.stringify({ ...tb, rtl: i18n.dir() === 'rtl' }),
      });
      if (!res.ok) throw new ApiError(res.status, 'export_failed');
      const blob = await res.blob();
      await saveFile(fileName(tb.title, 'xlsx'), blob, 'application/vnd.openxmlformats-officedocument.spreadsheetml.sheet');
    } catch (e) {
      notifyError(t, e);
    } finally {
      setBusy(false);
    }
  }

  return (
    <Menu position="bottom-end" withinPortal>
      <Menu.Target>
        <Button size="sm" variant="default" leftSection={<IconDownload size={16} />} loading={busy} disabled={disabled} className="no-print">
          {t('gap.export')}
        </Button>
      </Menu.Target>
      <Menu.Dropdown>
        <Menu.Item leftSection={<IconFileSpreadsheet size={16} />} onClick={() => void xlsx()}>{t('gap.exportXlsx')}</Menu.Item>
        <Menu.Item leftSection={<IconFileText size={16} />} onClick={() => void csv()}>{t('gap.exportCsv')}</Menu.Item>
        <Menu.Item leftSection={<IconPrinter size={16} />} onClick={() => void printPage()}>{t('gap.print')}</Menu.Item>
      </Menu.Dropdown>
    </Menu>
  );
}
