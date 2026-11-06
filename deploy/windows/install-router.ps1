# Installe le routeur de connexion seul (poste Prestige ou serveur d'accès), en service Windows.
# Prestige se connecte ensuite à l'adresse « listen » du routeur au lieu de l'adresse du serveur MariaDB.
param(
  [string]$Source = ".\prm-agent-windows-amd64.exe",
  [string]$InstallDir = "$env:ProgramFiles\PrestigeReplicaManager",
  [string]$DataDir = "$env:ProgramData\PrestigeReplicaManager"
)
$ErrorActionPreference = "Stop"
$svc = "PrestigeReplicaRouter"

New-Item -ItemType Directory -Force -Path $InstallDir, $DataDir | Out-Null
Copy-Item $Source "$InstallDir\prm-agent.exe" -Force

if (-not (Get-Service $svc -ErrorAction SilentlyContinue)) {
  New-Service -Name $svc -BinaryPathName "`"$InstallDir\prm-agent.exe`" router -config `"$DataDir\router.json`"" `
    -DisplayName "Prestige Replica Manager - Routeur de connexion" -StartupType Automatic `
    -Description "Dirige Prestige vers le seul serveur MariaDB accessible en écriture" | Out-Null
  sc.exe config $svc obj= "NT SERVICE\$svc" | Out-Null
  # Le routeur doit démarrer avant Prestige : démarrage automatique immédiat, relance rapide.
  sc.exe failure $svc reset= 3600 actions= restart/2000/restart/5000/restart/30000 | Out-Null
}
icacls $DataDir /grant "NT SERVICE\${svc}:(OI)(CI)R" | Out-Null

Write-Host "Placez router.json et mariadb.password dans $DataDir puis : Start-Service $svc"
Write-Host "Journal : Observateur d'événements, ou exécutez en console : & `"$InstallDir\prm-agent.exe`" router -config `"$DataDir\router.json`""
