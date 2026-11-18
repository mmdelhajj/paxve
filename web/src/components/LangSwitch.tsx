import { Select } from '@mantine/core';
import { IconLanguage } from '@tabler/icons-react';
import { useTranslation } from 'react-i18next';
import { applyLang, languages } from '../i18n';

export default function LangSwitch({ onChange }: { onChange?: (lng: string) => void }) {
  const { i18n } = useTranslation();
  return (
    <Select
      size="xs"
      w={120}
      aria-label="Language"
      leftSection={<IconLanguage size={14} />}
      value={i18n.language}
      allowDeselect={false}
      comboboxProps={{ withinPortal: true }}
      data={languages.map((l) => ({ value: l.code, label: l.label }))}
      onChange={(v) => {
        if (!v) return;
        applyLang(v);
        onChange?.(v);
      }}
    />
  );
}
