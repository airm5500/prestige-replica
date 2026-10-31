# Autorise le compte du service de l'agent à arrêter, démarrer et reconfigurer le service MariaDB.
# Nécessaire pour la reconstruction de réplique (Lot 2) et l'isolement d'un ancien primaire (Lot 4) :
# le compte virtuel « NT SERVICE\PrestigeReplicaAgent » n'a pas ces droits par défaut.
param(
  [string]$MariaDbService = "MariaDB",
  [string]$AgentService = "PrestigeReplicaAgent"
)
$ErrorActionPreference = "Stop"
$sid = ((sc.exe showsid $AgentService) -join "`n" | Select-String "S-1-5-80(-\d+)+").Matches[0].Value
$sddl = ((sc.exe sdshow $MariaDbService) | Where-Object { $_ -match "^D:" }) -join ""
if (-not $sddl) { throw "Descripteur de sécurité du service $MariaDbService introuvable" }
if ($sddl -like "*$sid*") { Write-Host "Droits déjà accordés à $AgentService sur $MariaDbService"; exit 0 }
# RP démarrer, WP arrêter, DC modifier la configuration (démarrage auto/manuel), LC/LO/CR/RC lecture de l'état.
$ace = "(A;;RPWPDCLCLOCRRC;;;$sid)"
$i = $sddl.IndexOf("S:")
$new = if ($i -ge 0) { $sddl.Insert($i, $ace) } else { $sddl + $ace }
sc.exe sdset $MariaDbService $new | Out-Null
if ($LASTEXITCODE -ne 0) { throw "sc sdset a échoué ($LASTEXITCODE)" }
Write-Host "Droits accordés à NT SERVICE\$AgentService sur le service $MariaDbService. Ancien descripteur : $sddl"
