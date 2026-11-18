<#
.SYNOPSIS
  Exports the data Paxve needs from a Magic database (SQL Server) into one zip file.

.DESCRIPTION
  READ-ONLY: this script only runs the SELECT queries written below. It never changes, deletes or backs up
  anything in Magic, and it never stores your password (Windows login is used by default; a SQL login is asked
  for with a secure prompt and kept in memory only).

  What is exported (one CSV file per table, then zipped):
    companies, branches, stations, boxes (Location), collectors (without their SMS gateway keys), plans (Sevice),
    chart of accounts, account holders and their phones / addresses, posting accounts, subscriptions (without
    VOD passwords, receiver and card numbers), bills of the last N months + every unpaid bill, and the journal
    (Movement) SUMMED per account, currency and year (not the 1.7 million lines one by one).

  Upload the zip in Paxve: Settings -> Import from Magic.

.EXAMPLE
  powershell -ExecutionPolicy Bypass -File .\Export-Magic.ps1
  powershell -ExecutionPolicy Bypass -File .\Export-Magic.ps1 -Server ".\SQLEXPRESS" -Database ManagmentDBElec -Months 24
  powershell -ExecutionPolicy Bypass -File .\Export-Magic.ps1 -Server ACCOUNTNEW -SqlLogin
#>
[CmdletBinding()]
param(
  [string]$Server = "",
  [string]$Database = "ManagmentDBElec",
  [ValidateRange(0, 120)][int]$Months = 24,
  [string]$Out = "",
  [switch]$SqlLogin,
  [switch]$NoLock
)

$ErrorActionPreference = 'Stop'
$ScriptVersion = 1
Add-Type -AssemblyName System.Data
Add-Type -AssemblyName System.IO.Compression.FileSystem

Write-Host ""
Write-Host "Paxve - export from Magic (read-only)" -ForegroundColor Cyan
Write-Host "--------------------------------------"
if ($Server -eq "") {
  $Server = Read-Host "SQL Server instance (examples: .  or  .\SQLEXPRESS  or  ACCOUNTNEW)"
  if ($Server -eq "") { $Server = "." }
}
if ($Database -notmatch '^[A-Za-z0-9_]+$') { throw "Invalid database name: $Database" }

$builder = New-Object System.Data.SqlClient.SqlConnectionStringBuilder
$builder["Data Source"] = $Server
$builder["Initial Catalog"] = $Database
$builder["Application Name"] = "Paxve export (read-only)"
$builder["Connect Timeout"] = 30
$credential = $null
if ($SqlLogin) {
  $c = Get-Credential -Message "SQL Server login for $Server (kept in memory only)"
  $pw = $c.Password.Copy()
  $pw.MakeReadOnly()
  $credential = New-Object System.Data.SqlClient.SqlCredential($c.UserName, $pw)
} else {
  $builder["Integrated Security"] = $true
}
$conn = New-Object System.Data.SqlClient.SqlConnection($builder.ConnectionString)
if ($credential) { $conn.Credential = $credential }
$conn.Open()
Write-Host "Connected to $Server / $Database" -ForegroundColor Green

$isolation = "READ COMMITTED"
if ($NoLock) { $isolation = "READ UNCOMMITTED" }

$stamp = Get-Date -Format "yyyyMMdd-HHmm"
if ($Out -eq "") { $Out = Join-Path ([Environment]::GetFolderPath("Desktop")) "magic-export-$stamp.zip" }
$work = Join-Path ([IO.Path]::GetTempPath()) ("paxve-magic-" + [Guid]::NewGuid().ToString("N"))
New-Item -ItemType Directory -Path $work | Out-Null

$inv = [Globalization.CultureInfo]::InvariantCulture
function Format-Value($v) {
  if ($v -is [DBNull] -or $null -eq $v) { return "" }
  if ($v -is [bool]) { if ($v) { return "1" } else { return "0" } }
  if ($v -is [DateTime]) { return $v.ToString("yyyy-MM-dd", $inv) }
  if ($v -is [single] -or $v -is [double]) { return $v.ToString("R", $inv) }
  if ($v -is [decimal]) { return $v.ToString($inv) }
  if ($v -is [byte[]]) { return "" }
  return [string]$v
}

$tables = [ordered]@{}
function Export-Query([string]$name, [string]$sql) {
  $trim = $sql.TrimStart()
  if (-not ($trim.StartsWith("SELECT", [StringComparison]::OrdinalIgnoreCase))) { throw "Refusing to run a non-SELECT statement" }
  if ($sql -match '(?i)\b(INSERT|UPDATE|DELETE|MERGE|DROP|ALTER|CREATE|TRUNCATE|EXEC|BACKUP|RESTORE|GRANT|DENY)\b') {
    throw "Refusing to run a statement that could change the database"
  }
  Write-Host ("  {0,-24}" -f $name) -NoNewline
  $cmd = $conn.CreateCommand()
  $cmd.CommandTimeout = 1800
  $cmd.CommandText = "SET TRANSACTION ISOLATION LEVEL $isolation; " + $sql
  $reader = $cmd.ExecuteReader()
  $path = Join-Path $work "$name.csv"
  $w = New-Object IO.StreamWriter($path, $false, (New-Object Text.UTF8Encoding($true)))
  $n = 0
  try {
    $cols = @()
    for ($i = 0; $i -lt $reader.FieldCount; $i++) { $cols += $reader.GetName($i) }
    $w.WriteLine((($cols | ForEach-Object { '"' + $_.Replace('"', '""') + '"' }) -join ","))
    $vals = New-Object object[] $reader.FieldCount
    while ($reader.Read()) {
      [void]$reader.GetValues($vals)
      $line = New-Object Text.StringBuilder
      for ($i = 0; $i -lt $vals.Length; $i++) {
        if ($i -gt 0) { [void]$line.Append(',') }
        [void]$line.Append('"').Append((Format-Value $vals[$i]).Replace('"', '""')).Append('"')
      }
      $w.WriteLine($line.ToString())
      $n++
    }
  } finally {
    $w.Close()
    $reader.Close()
  }
  $tables[$name] = $n
  Write-Host ("{0,10:N0} rows" -f $n)
}

Write-Host "Reading (SELECT only):"
Export-Query "Company"   "SELECT CompanyID, CompanyName, CompanyTypeID, Available FROM dbo.Company"
Export-Query "Branchs"   "SELECT BranchID, BranchName, CompanyID FROM dbo.Branchs"
Export-Query "Stations"  "SELECT StationID, Station, BranchID, Available FROM dbo.Stations"
Export-Query "Location"  "SELECT LocationID, Location, StationID, DefaultCollectorID, Available, Adress, LocOrder FROM dbo.Location"
Export-Query "Collector" "SELECT c.CollectorID, c.AccountNameID, c.CompanyID, c.Activate, c.DefaultC, c.AccountName, n.AccountName AS CollectorName
  FROM dbo.Collector c LEFT JOIN dbo.AccountNames n ON n.AccountNameID = c.AccountNameID"
Export-Query "Money"     "SELECT MoneyID, Money, DolBource, LLBource, CashBackLLBource FROM dbo.Money"
Export-Query "Sevice"    "SELECT SeviceID, TypeInscriptionID, Sevice, PowerService, CompanyID, BaseQt, BaseCost, Cost, NetCost, Discount, Available FROM dbo.Sevice"
Export-Query "LedgerAccount" "SELECT LedgerID, LedgerParentID, NameEn, NameAr, NameFr, BalanceTypeID, [Level], Used FROM dbo.LedgerAccount"
Export-Query "AccountNames"  "SELECT AccountNameID, AccountName, Available, AccountType FROM dbo.AccountNames"
Export-Query "AcountNamesInformation" "SELECT AccountNameID, TypeInformationID, Information FROM dbo.AcountNamesInformation WHERE TypeInformationID IN (1, 2, 3, 6, 7, 8)"
Export-Query "Accounts"  "SELECT LedgerID, AccountNameID, MoneyID, CompanyID, Freezed, Available FROM dbo.Accounts"
Export-Query "Subscrip"  "SELECT SubID, LedgerID, AccountNameID, MoneyID, CompanyID, ExecutiveCollectorID, LocationID, NumInLocation, SeviceID,
  TypeInscriptionID, TypePaymentSubID, DateBegin, Freezed, FreezedNoCharge, Activeted, Available, Maktab, SpeciaBaseCost, SpeciaCost,
  Adress, UserNet, CellPhone, HomePhone, ExpiredDate, RQ FROM dbo.Subscrip"
Export-Query "DataCustomer" ("SELECT IssalID, Annee, CompanyID, SubID, MonthID, YesNoPayed, NewCounter, OldCounter, BaseCost, Cost, FinalCost,
  Discount, ValuePayed, ValuePayedLBP, CashbackLBP, Bource, DateInvoiced, DateMustPayed, Deleted, Visible, SeviceID
  FROM dbo.DataCustomer
  WHERE (YesNoPayed = 0 AND ISNULL(Deleted, 0) = 0) OR DateInvoiced >= DATEADD(month, -" + [int]$Months + ", GETDATE())")
# The journal, summed per posting account x line currency x year (Magic counts only visible lines).
# AcctRateOK / UsdRateOK flag groups whose exchange rates look wrong, so Paxve can repair them.
$acctOK = "CASE WHEN m.MoneyID_DBCR = m.MoneyID THEN 1
    WHEN RTRIM(ma.Money) = 'USD' AND RTRIM(ml.Money) = 'LBP' AND m.DolBource BETWEEN 0.000001 AND 0.002 THEN 1
    WHEN RTRIM(ma.Money) = 'LBP' AND RTRIM(ml.Money) = 'USD' AND m.LLBource BETWEEN 500 AND 1000000 THEN 1
    ELSE 0 END"
$usdOK = "CASE WHEN RTRIM(ml.Money) = 'USD' AND m.DolBource BETWEEN 0.999 AND 1.001 THEN 1
    WHEN RTRIM(ml.Money) = 'LBP' AND m.DolBource BETWEEN 0.000001 AND 0.002 THEN 1
    ELSE 0 END"
$amt = "(CAST(ISNULL(m.DB, 0) AS float) - CAST(ISNULL(m.CR, 0) AS float))"
Export-Query "MovementBalances" ("SELECT m.CompanyID, m.LedgerID, m.AccountNameID, m.MoneyID, m.MoneyID_DBCR, YEAR(m.[Date]) AS Yr,
  $acctOK AS AcctRateOK, $usdOK AS UsdRateOK, COUNT(*) AS Lines,
  CAST(SUM($amt) AS decimal(38, 6)) AS SumRaw,
  CAST(SUM($amt * CAST(ISNULL(m.DolBource, 0) AS float)) AS decimal(38, 6)) AS SumUSD,
  CAST(SUM($amt * CAST(ISNULL(m.LLBource, 0) AS float)) AS decimal(38, 6)) AS SumLBP,
  CONVERT(char(10), MIN(m.[Date]), 120) AS MinDate, CONVERT(char(10), MAX(m.[Date]), 120) AS MaxDate
  FROM dbo.Movement m
  LEFT JOIN dbo.Money ma ON ma.MoneyID = m.MoneyID
  LEFT JOIN dbo.Money ml ON ml.MoneyID = m.MoneyID_DBCR
  WHERE m.Visible = 1
  GROUP BY m.CompanyID, m.LedgerID, m.AccountNameID, m.MoneyID, m.MoneyID_DBCR, YEAR(m.[Date]), $acctOK, $usdOK")
$conn.Close()

$manifest = [ordered]@{
  format         = "paxve-magic-export"
  version        = 1
  script_version = $ScriptVersion
  exported_at    = (Get-Date).ToUniversalTime().ToString("yyyy-MM-ddTHH:mm:ssZ", $inv)
  database       = $Database
  months         = $Months
  tables         = $tables
}
$manifestPath = Join-Path $work "manifest.json"
[IO.File]::WriteAllText($manifestPath, ($manifest | ConvertTo-Json -Depth 4), (New-Object Text.UTF8Encoding($false)))

if (Test-Path $Out) { Remove-Item $Out -Force }
[IO.Compression.ZipFile]::CreateFromDirectory($work, $Out, [IO.Compression.CompressionLevel]::Optimal, $false)
Remove-Item $work -Recurse -Force
$hash = (Get-FileHash $Out -Algorithm SHA256).Hash.ToLower()
$size = (Get-Item $Out).Length

Write-Host ""
Write-Host "Done." -ForegroundColor Green
Write-Host ("File:   {0}" -f $Out)
Write-Host ("Size:   {0:N1} MB" -f ($size / 1MB))
Write-Host ("SHA256: {0}" -f $hash)
Write-Host ""
Write-Host "Next: open Paxve -> Settings -> Import from Magic, and upload this file."
Write-Host "The file contains your customers' names and phone numbers: keep it private and delete it after the import."
