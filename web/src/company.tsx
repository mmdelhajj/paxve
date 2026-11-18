import { createContext, useCallback, useContext, useEffect, useMemo, useState, type ReactNode } from 'react';
import { api, type Company, type Currency } from './api';

interface CompanyCtx {
  companies: Company[];
  company: Company | null;
  setCompanyId: (id: string) => void;
  reload: () => void;
  currencies: Currency[];
  decimalsOf: (code: string) => number;
}

const Ctx = createContext<CompanyCtx | null>(null);

function savedId(): string | null {
  try {
    return localStorage.getItem('paxve.company');
  } catch {
    return null;
  }
}

export function CompanyProvider({ children }: { children: ReactNode }) {
  const [companies, setCompanies] = useState<Company[]>([]);
  const [currencies, setCurrencies] = useState<Currency[]>([]);
  const [selected, setSelected] = useState<string | null>(savedId);

  const reload = useCallback(() => {
    api<Company[]>('/companies').then(setCompanies).catch(() => {});
  }, []);

  useEffect(() => {
    reload();
    api<Currency[]>('/currencies').then(setCurrencies).catch(() => {});
  }, [reload]);

  const value = useMemo<CompanyCtx>(() => {
    const active = companies.filter((c) => c.is_active);
    const company = active.find((c) => c.id === selected) ?? active[0] ?? null;
    const dec = new Map(currencies.map((c) => [c.code, c.decimals]));
    return {
      companies: active,
      company,
      setCompanyId: (id: string) => {
        setSelected(id);
        try {
          localStorage.setItem('paxve.company', id);
        } catch {
          /* storage unavailable */
        }
      },
      reload,
      currencies,
      decimalsOf: (code: string) => dec.get(code) ?? 2,
    };
  }, [companies, currencies, selected, reload]);

  return <Ctx.Provider value={value}>{children}</Ctx.Provider>;
}

export function useCompany(): CompanyCtx {
  const v = useContext(Ctx);
  if (!v) throw new Error('useCompany outside CompanyProvider');
  return v;
}
