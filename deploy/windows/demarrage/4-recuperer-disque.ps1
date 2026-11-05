# Récupère les bases MariaDB d'un PC mort, à partir de son disque branché sur ce PC.
# La source n'est jamais modifiée : tout se fait sur une copie.
# PowerShell en administrateur, dans ce dossier :
#   .\4-recuperer-disque.ps1 -Source "E:\Program Files\MariaDB 10.6\data"
# Options : -Destination D:\Recuperation (prévoir 2 fois la taille du dossier data)
#           -Bin "E:\Program Files\MariaDB 10.6\bin" (si le dossier bin n'est pas à côté de data)
#           -Diagnostic (inventaire et diagnostic seulement)
param(
  [Parameter(Mandatory = $true)][string]$Source,
  [string]$Destination = "C:\Recuperation",
  [string]$Bin = "",
  [switch]$Diagnostic
)
$ErrorActionPreference = "Stop"
$here = Split-Path -Parent $MyInvocation.MyCommand.Path

$isAdmin = ([Security.Principal.WindowsPrincipal][Security.Principal.WindowsIdentity]::GetCurrent()).IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)
if (-not $isAdmin) { Write-Host "Relancez PowerShell en administrateur (clic droit > Exécuter en tant qu'administrateur)." -ForegroundColor Red; exit 1 }
if (-not (Test-Path $Source)) { Write-Host "Dossier introuvable : $Source" -ForegroundColor Red; exit 1 }

$agent = Join-Path $here "prm-agent.exe"
if (-not (Test-Path $agent)) { $agent = "$env:ProgramFiles\PrestigeReplicaManager\prm-agent.exe" }
if (-not (Test-Path $agent)) { Write-Host "prm-agent.exe introuvable (il doit être dans le dossier du kit)." -ForegroundColor Red; exit 1 }

$stamp = Get-Date -Format "yyyy-MM-dd_HHmm"
$root = Join-Path $Destination $stamp
$work = Join-Path $root "travail"
$out = Join-Path $root "resultat"
New-Item -ItemType Directory -Force -Path $root | Out-Null

# Place disponible : au moins 2 fois la taille de la source.
$size = (Get-ChildItem -LiteralPath $Source -Recurse -File -ErrorAction SilentlyContinue | Measure-Object Length -Sum).Sum
$drive = (Get-Item $root).PSDrive
if (-not $Diagnostic -and $drive.Free -lt 2 * $size) {
  Write-Host ("Place insuffisante sur {0}: : {1:N1} Go libres, {2:N1} Go nécessaires. Utiliser -Destination sur un autre disque." -f $drive.Name, ($drive.Free / 1GB), (2 * $size / 1GB)) -ForegroundColor Red
  exit 1
}

$cmdArgs = @("recover", "-source", $Source, "-out", $out)
if ($Diagnostic) { $cmdArgs += "-diagnostic" } else { $cmdArgs += @("-work", $work) }
if ($Bin -ne "") { $cmdArgs += @("-bin", $Bin) }

Write-Host "Récupération en cours, cela peut durer longtemps sur une grosse base. Ne pas fermer cette fenêtre." -ForegroundColor Cyan
& $agent @args
$code = $LASTEXITCODE

$rapport = Join-Path $out "RAPPORT.txt"
if (Test-Path $rapport) { Start-Process notepad.exe $rapport }
switch ($code) {
  0 { Write-Host "Terminé : tout est récupéré. Résultat : $out" -ForegroundColor Green }
  3 { Write-Host "Terminé, mais des tables n'ont pas pu être récupérées : voir le rapport. Résultat : $out" -ForegroundColor Yellow }
  default { Write-Host "Échec : voir le message ci-dessus et le rapport, et envoyer une capture d'écran." -ForegroundColor Red }
}
if (-not $Diagnostic -and $code -ne 1) {
  Write-Host "Le dossier de travail $work peut être supprimé une fois la restauration vérifiée."
}
