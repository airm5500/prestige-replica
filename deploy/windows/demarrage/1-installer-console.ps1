# Étape 1 : installe la console Prestige Replica Manager sur ce PC, sans Docker.
# Tout est inclus dans le dossier « console » : Java, la base PostgreSQL de la console, l'application.
# PowerShell en administrateur, dans ce dossier :  .\1-installer-console.ps1
# Deux services Windows sont créés : PrestigeReplicaDB (base interne, port 5433) et PrestigeReplicaConsole (http://127.0.0.1:9090).
param([int]$Port = 9090, [int]$PortBase = 5433)
$ErrorActionPreference = "Stop"
$here = Split-Path -Parent $MyInvocation.MyCommand.Path

$isAdmin = ([Security.Principal.WindowsPrincipal][Security.Principal.WindowsIdentity]::GetCurrent()).IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)
if (-not $isAdmin) { Write-Host "Relancez PowerShell en administrateur (clic droit > Exécuter en tant qu'administrateur)." -ForegroundColor Red; exit 1 }

$inst = "$env:ProgramFiles\PrestigeReplicaManager\console"
$data = "$env:ProgramData\PrestigeReplicaManager\console"
$pgdata = "$data\pgdata"
$dbSvc = "PrestigeReplicaDB"
$appSvc = "PrestigeReplicaConsole"
$utf8 = New-Object System.Text.UTF8Encoding($false)
function New-Secret([int]$n) { -join ((48..57 + 65..90 + 97..122) | Get-Random -Count $n | ForEach-Object { [char]$_ }) }

foreach ($p in @($Port, $PortBase)) {
  if (Get-NetTCPConnection -LocalPort $p -State Listen -ErrorAction SilentlyContinue) {
    if (-not (Get-Service $appSvc, $dbSvc -ErrorAction SilentlyContinue)) {
      Write-Host "Le port $p est déjà utilisé sur ce PC. Relancez avec un autre port, par ex. : .\1-installer-console.ps1 -Port 9091 -PortBase 5434" -ForegroundColor Red; exit 1
    }
  }
}

# 1. Fichiers
Write-Host "1/5 Copie des fichiers dans $inst ..."
# Réinstallation : arrêter les services pour pouvoir remplacer leurs fichiers.
if (Get-Service $appSvc -ErrorAction SilentlyContinue) { Stop-Service $appSvc -ErrorAction SilentlyContinue }
if (Get-Service $dbSvc -ErrorAction SilentlyContinue) { Stop-Service $dbSvc -ErrorAction SilentlyContinue }
New-Item -ItemType Directory -Force -Path $inst, $data | Out-Null
Copy-Item -Path "$here\console\*" -Destination $inst -Recurse -Force

# 2. Base interne de la console (PostgreSQL portable, écoute uniquement sur 127.0.0.1)
$secrets = "$data\secrets.json"
if (Test-Path $secrets) { $s = Get-Content $secrets -Raw | ConvertFrom-Json }
else {
  $s = [pscustomobject]@{ db = (New-Secret 24); session = (New-Secret 48); admin = (New-Secret 16) }
  [IO.File]::WriteAllText($secrets, ($s | ConvertTo-Json), $utf8)
}
if (-not (Test-Path "$pgdata\PG_VERSION")) {
  Write-Host "2/5 Création de la base interne..."
  New-Item -ItemType Directory -Force -Path $pgdata | Out-Null
  $pw = "$data\pw.tmp"
  [IO.File]::WriteAllText($pw, $s.db, $utf8)
  & "$inst\pgsql\bin\initdb.exe" -D $pgdata -U prm "--pwfile=$pw" -E UTF8 --no-locale -A scram-sha-256 | Out-Host
  $rc = $LASTEXITCODE
  Remove-Item $pw -Force
  if ($rc -ne 0) { Write-Host "Échec de la création de la base (voir ci-dessus)." -ForegroundColor Red; exit 1 }
  Add-Content -Path "$pgdata\postgresql.conf" -Value "`r`nlisten_addresses = '127.0.0.1'`r`nport = $PortBase`r`n" -Encoding ASCII
} else { Write-Host "2/5 Base interne déjà créée : conservée." }

# Compte du service de la base : NETWORK SERVICE (S-1-5-20), avec droits sur le dossier de données uniquement.
icacls $data /inheritance:r /grant:r "*S-1-5-18:(OI)(CI)F" "*S-1-5-32-544:(OI)(CI)F" | Out-Null
icacls $pgdata /grant "*S-1-5-20:(OI)(CI)M" | Out-Null
if (-not (Get-Service $dbSvc -ErrorAction SilentlyContinue)) {
  & "$inst\pgsql\bin\pg_ctl.exe" register -N $dbSvc -U "NT AUTHORITY\NetworkService" -D $pgdata -S auto -w | Out-Host
}
Write-Host "3/5 Démarrage de la base interne..."
Start-Service $dbSvc

# 3. Service de la console (WinSW), secrets passés en variables d'environnement
Write-Host "4/5 Installation du service de la console..."
$xml = @"
<service>
  <id>$appSvc</id>
  <name>Prestige Replica Manager - Console</name>
  <description>Console web de supervision et de réplication MariaDB (http://127.0.0.1:$Port)</description>
  <executable>%BASE%\..\runtime\bin\java.exe</executable>
  <arguments>-Xmx512m -XX:+ExitOnOutOfMemoryError -jar "%BASE%\..\app\quarkus-run.jar"</arguments>
  <workingdirectory>%BASE%\..\app</workingdirectory>
  <depend>$dbSvc</depend>
  <startmode>Automatic</startmode>
  <onfailure action="restart" delay="10 sec"/>
  <onfailure action="restart" delay="60 sec"/>
  <log mode="roll-by-size"><sizeThreshold>10240</sizeThreshold><keepFiles>5</keepFiles></log>
  <logpath>$data\logs</logpath>
  <env name="PRM_HTTP_PORT" value="$Port"/>
  <env name="PRM_DB_URL" value="jdbc:postgresql://127.0.0.1:$PortBase/postgres"/>
  <env name="PRM_DB_USER" value="prm"/>
  <env name="PRM_DB_PASSWORD" value="$($s.db)"/>
  <env name="PRM_SESSION_KEY" value="$($s.session)"/>
  <env name="PRM_BOOTSTRAP_ADMIN_PASSWORD" value="$($s.admin)"/>
</service>
"@
[IO.File]::WriteAllText("$inst\service\$appSvc.xml", $xml, $utf8)
# Le fichier du service contient des secrets : lisible par SYSTEM et Administrateurs seulement.
icacls "$inst\service" /inheritance:r /grant:r "*S-1-5-18:(OI)(CI)F" "*S-1-5-32-544:(OI)(CI)F" | Out-Null
if (-not (Get-Service $appSvc -ErrorAction SilentlyContinue)) { & "$inst\service\$appSvc.exe" install | Out-Host }
Start-Service $appSvc

Write-Host "5/5 Attente de la console (jusqu'à 2 minutes)..."
$ok = $false
for ($i = 0; $i -lt 60; $i++) {
  try { Invoke-WebRequest -UseBasicParsing -Uri "http://127.0.0.1:$Port/" -TimeoutSec 2 | Out-Null; $ok = $true; break } catch { Start-Sleep -Seconds 2 }
}
$note = Join-Path $here "MOT-DE-PASSE-ADMIN.txt"
[IO.File]::WriteAllText($note, "Console : http://127.0.0.1:$Port`r`nIdentifiant : admin`r`nMot de passe : $($s.admin)`r`n(à changer après la première connexion, puis supprimer ce fichier)`r`n", $utf8)
if ($ok) {
  Start-Process "http://127.0.0.1:$Port/"
  Write-Host "Console prête : http://127.0.0.1:$Port  —  identifiant admin, mot de passe : $($s.admin)" -ForegroundColor Green
  Write-Host "(aussi enregistré dans $note)"
} else {
  Write-Host "La console ne répond pas encore. Journal : $data\logs" -ForegroundColor Red
}
