# Installe l'agent comme service Windows natif (à exécuter en administrateur).
# Le service tourne sous « NT SERVICE\PrestigeReplicaAgent » (compte virtuel sans session interactive).
param(
  [string]$Source = ".\prm-agent-windows-amd64.exe",
  [string]$InstallDir = "$env:ProgramFiles\PrestigeReplicaManager",
  [string]$DataDir = "$env:ProgramData\PrestigeReplicaManager"
)
$ErrorActionPreference = "Stop"
$svc = "PrestigeReplicaAgent"

New-Item -ItemType Directory -Force -Path $InstallDir, $DataDir | Out-Null
Copy-Item $Source "$InstallDir\prm-agent.exe" -Force

if (-not (Get-Service $svc -ErrorAction SilentlyContinue)) {
  New-Service -Name $svc -BinaryPathName "`"$InstallDir\prm-agent.exe`" run -config `"$DataDir\agent.json`"" `
    -DisplayName "Prestige Replica Manager - Agent" -StartupType Automatic `
    -Description "Supervision en lecture seule de la réplication MariaDB" | Out-Null
  sc.exe config $svc obj= "NT SERVICE\$svc" | Out-Null
  sc.exe config $svc start= delayed-auto | Out-Null
  # Redémarrage après échec : 3 tentatives espacées, puis arrêt (pas de boucle infinie).
  sc.exe failure $svc reset= 86400 actions= restart/10000/restart/60000/restart/300000 | Out-Null
}

# ACL : seuls SYSTEM, Administrateurs et le compte du service lisent la configuration et les secrets.
icacls $DataDir /inheritance:r /grant:r "SYSTEM:(OI)(CI)F" "Administrators:(OI)(CI)F" "NT SERVICE\${svc}:(OI)(CI)R" | Out-Null

Write-Host "Placez agent.json, agent.token et mariadb.password dans $DataDir puis : Start-Service $svc"
Write-Host "Vérification : & `"$InstallDir\prm-agent.exe`" check -config `"$DataDir\agent.json`""
