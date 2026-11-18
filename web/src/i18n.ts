import i18n from 'i18next';
import { initReactI18next } from 'react-i18next';
import type { Locale } from './api';

const en = {
  app: { name: 'Paxve', tagline: 'Accounting and billing for subscription businesses' },
  login: { title: 'Sign in to your account', email: 'Email', password: 'Password', submit: 'Sign in' },
  nav: { dashboard: 'Dashboard', companies: 'Companies', users: 'Users', audit: 'Audit log' },
  common: {
    save: 'Save', cancel: 'Cancel', active: 'Active', inactive: 'Inactive', status: 'Status',
    created: 'Created', saved: 'Saved', empty: 'Nothing here yet', logout: 'Sign out',
    changePassword: 'Change password', language: 'Language', you: 'you',
  },
  roles: { admin: 'Admin', accountant: 'Accountant', data_entry: 'Data entry', read_only: 'Read only' },
  dashboard: {
    welcome: 'Welcome, {{name}}', companies: 'Companies', users: 'Users', role: 'Your role',
    nextTitle: 'Coming next',
    next1: 'Chart of accounts (Lebanese PCG)',
    next2: 'Journal entries in USD and LBP, always balanced',
    next3: 'Daily exchange rates',
    next4: 'Subscribers, meters and monthly bills',
    next5: 'Collector app with Arabic receipts',
  },
  companies: {
    title: 'Companies', new: 'New company', nameEn: 'Name (English)', nameAr: 'Name (Arabic)',
    currencies: 'Currencies',
  },
  users: {
    title: 'Users', new: 'New user', fullName: 'Full name', email: 'Email', role: 'Role',
    password: 'Initial password (at least 10 characters)', language: 'Interface language',
  },
  audit: {
    title: 'Audit log', when: 'When', who: 'User', action: 'Action', record: 'Record', changes: 'Changes',
    system: 'System', passwordChanged: 'password changed',
    actions: { insert: 'Created', update: 'Updated', delete: 'Deleted', login: 'Signed in' },
    entities: { companies: 'Company', users: 'User', tenants: 'Organisation' },
  },
  password: { current: 'Current password', new: 'New password (at least 10 characters)' },
  errors: {
    invalid_credentials: 'Wrong email or password',
    too_many_attempts: 'Too many attempts. Try again in 15 minutes.',
    forbidden: "You don't have permission to do this",
    unauthorized: 'Please sign in again',
    email_taken: 'This email is already used',
    name_taken: 'A company with this name already exists',
    weak_password: 'Password must be at least 10 characters',
    wrong_password: 'Current password is wrong',
    cannot_change_self: "You can't change your own role or status",
    bad_request: 'Please check the fields',
    not_found: 'Not found',
    server_error: 'Something went wrong. Please try again.',
  },
};

const ar: typeof en = {
  app: { name: 'Paxve', tagline: 'المحاسبة والفوترة لشركات الاشتراكات' },
  login: { title: 'تسجيل الدخول إلى حسابك', email: 'البريد الإلكتروني', password: 'كلمة المرور', submit: 'دخول' },
  nav: { dashboard: 'لوحة التحكم', companies: 'الشركات', users: 'المستخدمون', audit: 'سجل التدقيق' },
  common: {
    save: 'حفظ', cancel: 'إلغاء', active: 'فعّال', inactive: 'موقوف', status: 'الحالة',
    created: 'تاريخ الإنشاء', saved: 'تم الحفظ', empty: 'لا يوجد شيء بعد', logout: 'تسجيل الخروج',
    changePassword: 'تغيير كلمة المرور', language: 'اللغة', you: 'أنت',
  },
  roles: { admin: 'مدير', accountant: 'محاسب', data_entry: 'إدخال بيانات', read_only: 'قراءة فقط' },
  dashboard: {
    welcome: 'أهلاً، {{name}}', companies: 'الشركات', users: 'المستخدمون', role: 'دورك',
    nextTitle: 'قريباً',
    next1: 'دليل الحسابات (الخطة المحاسبية اللبنانية)',
    next2: 'القيود اليومية بالدولار والليرة، متوازنة دائماً',
    next3: 'أسعار الصرف اليومية',
    next4: 'المشتركون والعدادات والفواتير الشهرية',
    next5: 'تطبيق الجباة مع إيصالات بالعربية',
  },
  companies: {
    title: 'الشركات', new: 'شركة جديدة', nameEn: 'الاسم (إنكليزي)', nameAr: 'الاسم (عربي)',
    currencies: 'العملات',
  },
  users: {
    title: 'المستخدمون', new: 'مستخدم جديد', fullName: 'الاسم الكامل', email: 'البريد الإلكتروني', role: 'الدور',
    password: 'كلمة مرور أولية (10 أحرف على الأقل)', language: 'لغة الواجهة',
  },
  audit: {
    title: 'سجل التدقيق', when: 'الوقت', who: 'المستخدم', action: 'العملية', record: 'السجل', changes: 'التغييرات',
    system: 'النظام', passwordChanged: 'تم تغيير كلمة المرور',
    actions: { insert: 'إنشاء', update: 'تعديل', delete: 'حذف', login: 'تسجيل دخول' },
    entities: { companies: 'شركة', users: 'مستخدم', tenants: 'المؤسسة' },
  },
  password: { current: 'كلمة المرور الحالية', new: 'كلمة المرور الجديدة (10 أحرف على الأقل)' },
  errors: {
    invalid_credentials: 'البريد الإلكتروني أو كلمة المرور غير صحيحة',
    too_many_attempts: 'محاولات كثيرة. حاول مجدداً بعد 15 دقيقة.',
    forbidden: 'ليست لديك صلاحية للقيام بذلك',
    unauthorized: 'يرجى تسجيل الدخول مجدداً',
    email_taken: 'هذا البريد مستخدم مسبقاً',
    name_taken: 'توجد شركة بهذا الاسم',
    weak_password: 'كلمة المرور يجب أن تكون 10 أحرف على الأقل',
    wrong_password: 'كلمة المرور الحالية غير صحيحة',
    cannot_change_self: 'لا يمكنك تغيير دورك أو حالتك',
    bad_request: 'يرجى التحقق من الحقول',
    not_found: 'غير موجود',
    server_error: 'حدث خطأ. حاول مجدداً.',
  },
};

function savedLang(): Locale {
  try {
    return localStorage.getItem('paxve.lang') === 'en' ? 'en' : 'ar';
  } catch {
    return 'ar';
  }
}

i18n.use(initReactI18next).init({
  resources: { en: { translation: en }, ar: { translation: ar } },
  lng: savedLang(),
  fallbackLng: 'en',
  interpolation: { escapeValue: false },
});

export function applyLang(lng: Locale) {
  if (i18n.language !== lng) void i18n.changeLanguage(lng);
  document.documentElement.lang = lng;
  document.documentElement.dir = lng === 'ar' ? 'rtl' : 'ltr';
  try {
    localStorage.setItem('paxve.lang', lng);
  } catch {
    /* storage unavailable */
  }
}

export default i18n;
