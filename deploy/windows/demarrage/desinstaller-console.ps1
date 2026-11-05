# Retire les services de la console (PowerShell en administrateur). Les données restent dans
# C:\ProgramData\PrestigeReplicaManager\console ; supprimez ce dossier pour tout effacer.
$ErrorActionPreference = "Continue"
$inst = "$env:ProgramFiles\PrestigeReplicaManager\console"
Stop-Service PrestigeReplicaConsole -ErrorAction SilentlyContinue
if (Test-Path "$inst\service\PrestigeReplicaConsole.exe") { & "$inst\service\PrestigeReplicaConsole.exe" uninstall }
Stop-Service PrestigeReplicaDB -ErrorAction SilentlyContinue
if (Test-Path "$inst\pgsql\bin\pg_ctl.exe") { & "$inst\pgsql\bin\pg_ctl.exe" unregister -N PrestigeReplicaDB }
Write-Host "Services retirés."
