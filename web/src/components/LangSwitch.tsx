import { SegmentedControl } from '@mantine/core';
import { useTranslation } from 'react-i18next';
import type { Locale } from '../api';
import { applyLang } from '../i18n';

export default function LangSwitch({ onChange }: { onChange?: (lng: Locale) => void }) {
  const { i18n } = useTranslation();
  return (
    <SegmentedControl
      size="xs"
      value={i18n.language}
      onChange={(v) => {
        const lng = v as Locale;
        applyLang(lng);
        onChange?.(lng);
      }}
      data={[
        { value: 'ar', label: 'عربي' },
        { value: 'en', label: 'EN' },
      ]}
    />
  );
}
