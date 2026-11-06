# Étape 3 : installe l'agent (service Windows) sur le PC où tourne MariaDB, en lecture seule.
# PowerShell en administrateur, dans ce dossier :
#   .\3-installer-agent.ps1 -Jeton "prm_..." -MotDePasseMariaDB "le mot de passe de l'étape 2"
# Le jeton est affiché par la console quand vous enregistrez le serveur (Paramètres > Serveurs).
param(
  [Parameter(Mandatory = $true)][string]$Jeton,
  [Parameter(Mandatory = $true)][string]$MotDePasseMariaDB,
  [string]$Console = "http://127.0.0.1:9090",
  [string]$AdresseMariaDB = "127.0.0.1:3306"
)
$ErrorActionPreference = "Stop"
$here = Split-Path -Parent $MyInvocation.MyCommand.Path
$svc = "PrestigeReplicaAgent"
$installDir = "$env:ProgramFiles\PrestigeReplicaManager"
$dataDir = "$env:ProgramData\PrestigeReplicaManager"

$isAdmin = ([Security.Principal.WindowsPrincipal][Security.Principal.WindowsIdentity]::GetCurrent()).IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)
if (-not $isAdmin) { Write-Host "Relancez PowerShell en administrateur (clic droit > Exécuter en tant qu'administrateur)." -ForegroundColor Red; exit 1 }

New-Item -ItemType Directory -Force -Path $installDir, $dataDir | Out-Null
if (Get-Service $svc -ErrorAction SilentlyContinue) { Stop-Service $svc -ErrorAction SilentlyContinue }
Copy-Item (Join-Path $here "prm-agent.exe") "$installDir\prm-agent.exe" -Force

$utf8 = New-Object System.Text.UTF8Encoding($false)
[IO.File]::WriteAllText("$dataDir\agent.token", $Jeton.Trim(), $utf8)
[IO.File]::WriteAllText("$dataDir\mariadb.password", $MotDePasseMariaDB, $utf8)
$cfg = [ordered]@{
  controlPlaneUrl = $Console
  agentTokenFile  = "$dataDir\agent.token"
  mariadb         = [ordered]@{ address = $AdresseMariaDB; user = "prm_monitor"; passwordFile = "$dataDir\mariadb.password" }
  intervalSeconds = 30
}
[IO.File]::WriteAllText("$dataDir\agent.json", ($cfg | ConvertTo-Json -Depth 4), $utf8)

Write-Host "Vérification de la configuration et de la connexion à MariaDB..."
& "$installDir\prm-agent.exe" check -config "$dataDir\agent.json"
if ($LASTEXITCODE -ne 0) { Write-Host "La vérification a échoué : corrigez le point signalé ci-dessus puis relancez." -ForegroundColor Red; exit 1 }

if (-not (Get-Service $svc -ErrorAction SilentlyContinue)) {
  New-Service -Name $svc -BinaryPathName "`"$installDir\prm-agent.exe`" run -config `"$dataDir\agent.json`"" `
    -DisplayName "Prestige Replica Manager - Agent" -StartupType Automatic `
    -Description "Supervision en lecture seule de la réplication MariaDB" | Out-Null
  sc.exe config $svc obj= "NT SERVICE\$svc" | Out-Null
  sc.exe failure $svc reset= 86400 actions= restart/10000/restart/60000/restart/300000 | Out-Null
}
icacls $dataDir /inheritance:r /grant:r "*S-1-5-18:(OI)(CI)F" "*S-1-5-32-544:(OI)(CI)F" "NT SERVICE\${svc}:(OI)(CI)R" | Out-Null
Start-Service $svc
Write-Host "Agent démarré. Le serveur apparaît dans la console (page Parc) d'ici 30 secondes." -ForegroundColor Green
