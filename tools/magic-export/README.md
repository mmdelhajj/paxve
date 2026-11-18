# Magic export (read-only)

`Export-Magic.ps1` reads a Magic database (SQL Server 2008 R2 or newer) and writes **one zip file** that
Paxve imports under **Settings → Import from Magic**.

- It runs `SELECT` queries only and refuses anything else.
- It never changes Magic.
- It never saves a password: it uses your Windows login, or asks for a SQL login with `-SqlLogin`.
- It leaves out VOD passwords, card and receiver numbers, the collectors' SMS keys and the `Users` table.

## Steps

1. Copy `Export-Magic.ps1` to the computer where Magic runs (or any computer that reaches its SQL Server).
2. Open **PowerShell** in that folder and run:

   ```powershell
   powershell -ExecutionPolicy Bypass -File .\Export-Magic.ps1
   ```

3. Type the SQL Server name when asked (`.`, `.\SQLEXPRESS`, `ACCOUNTNEW`…).
4. Wait for **Done.** The file `magic-export-YYYYMMDD-HHMM.zip` is on the Desktop.
5. In Paxve: **Settings → Import from Magic** → upload the file → check each company → **Import**.

Other examples:

```powershell
# another database name, 36 months of bill history, a SQL login
powershell -ExecutionPolicy Bypass -File .\Export-Magic.ps1 -Server ".\SQLEXPRESS" -Database ManagmentDBElec -Months 36 -SqlLogin
# never block Magic users while exporting (reads uncommitted data)
powershell -ExecutionPolicy Bypass -File .\Export-Magic.ps1 -NoLock
```

Run the export when nobody is typing in Magic, so the bills and the journal match. The zip contains
customer names and phone numbers: keep it private and delete it once it is imported.

Details of every table and rule: [docs/MAGIC_MIGRATION.md](../../docs/MAGIC_MIGRATION.md).

---

# التصدير من ماجيك (قراءة فقط)

السكربت `Export-Magic.ps1` يقرأ قاعدة بيانات ماجيك (SQL Server 2008 R2 أو أحدث) ويكتب **ملف zip واحداً**
تستورده في Paxve من **الإعدادات ← الاستيراد من ماجيك**.

- ينفّذ أوامر `SELECT` فقط ويرفض أي أمر آخر.
- لا يغيّر شيئاً في ماجيك.
- لا يحفظ أي كلمة سر: يستعمل حساب ويندوز، أو يسأل عن حساب SQL إذا أضفت `-SqlLogin`.
- لا يصدّر كلمات سر VOD ولا أرقام البطاقات والريسيفرات ولا مفاتيح رسائل الجباة ولا جدول المستخدمين.

## الخطوات

1. انسخ الملف `Export-Magic.ps1` إلى الكمبيوتر الذي يعمل عليه ماجيك (أو أي كمبيوتر يصل إلى SQL Server الخاص به).
2. افتح **PowerShell** في ذلك المجلد ونفّذ:

   ```powershell
   powershell -ExecutionPolicy Bypass -File .\Export-Magic.ps1
   ```

3. اكتب اسم SQL Server عند السؤال (`.` أو `.\SQLEXPRESS` أو `ACCOUNTNEW`…).
4. انتظر ظهور **Done.**، فيكون الملف `magic-export-YYYYMMDD-HHMM.zip` على سطح المكتب.
5. في Paxve: **الإعدادات ← الاستيراد من ماجيك** ← ارفع الملف ← راجع كل شركة ← **استيراد**.

شغّل التصدير حين لا يكون أحد يُدخل بيانات في ماجيك، حتى تتطابق الفواتير مع القيود. الملف يحتوي أسماء
الزبائن وأرقام هواتفهم: احتفظ به بشكل خاص واحذفه بعد الاستيراد.
