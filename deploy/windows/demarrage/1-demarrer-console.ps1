# Étape 1 : démarre la console Prestige Replica Manager sur ce PC (Docker Desktop requis).
# Clic droit > « Exécuter avec PowerShell ». La console s'ouvre ensuite sur http://127.0.0.1:9090
$ErrorActionPreference = "Stop"
$here = Split-Path -Parent $MyInvocation.MyCommand.Path
Set-Location $here

if (-not (Get-Command docker -ErrorAction SilentlyContinue)) {
  Write-Host "Docker Desktop n'est pas installé. Installez-le depuis https://www.docker.com/products/docker-desktop/ puis relancez ce script." -ForegroundColor Red
  Read-Host "Entrée pour fermer"; exit 1
}
docker info *> $null
if ($LASTEXITCODE -ne 0) {
  Write-Host "Docker Desktop est installé mais pas démarré. Lancez-le (icône baleine), attendez qu'il soit prêt, puis relancez ce script." -ForegroundColor Red
  Read-Host "Entrée pour fermer"; exit 1
}

# Sources : décompressées une seule fois à côté du script.
$src = Join-Path $here "prestige-replica"
if (-not (Test-Path $src)) {
  Write-Host "Décompression des sources..."
  Expand-Archive -Path (Join-Path $here "prestige-replica-source.zip") -DestinationPath $here
}

# Secrets générés une seule fois et gardés dans deploy\compose\.env
$envFile = Join-Path $src "deploy\compose\.env"
function New-Secret([int]$n) { -join ((48..57 + 65..90 + 97..122) | Get-Random -Count $n | ForEach-Object { [char]$_ }) }
if (-not (Test-Path $envFile)) {
  $admin = New-Secret 16
  $lines = @(
    "PRM_DB_PASSWORD=$(New-Secret 24)",
    "PRM_SESSION_KEY=$(New-Secret 48)",
    "PRM_BOOTSTRAP_ADMIN_PASSWORD=$admin"
  )
  [IO.File]::WriteAllLines($envFile, $lines)
  $note = Join-Path $here "MOT-DE-PASSE-ADMIN.txt"
  [IO.File]::WriteAllText($note, "Console : http://127.0.0.1:9090`r`nIdentifiant : admin`r`nMot de passe : $admin`r`n")
  Write-Host "Mot de passe de la console (aussi enregistré dans MOT-DE-PASSE-ADMIN.txt) : admin / $admin" -ForegroundColor Yellow
}

Write-Host "Construction et démarrage (10 à 15 minutes la première fois, quelques secondes ensuite)..."
docker compose -f (Join-Path $src "deploy\compose\compose.yaml") --env-file $envFile up -d --build
if ($LASTEXITCODE -ne 0) { Write-Host "Échec du démarrage : copiez le message ci-dessus et envoyez-le." -ForegroundColor Red; Read-Host "Entrée pour fermer"; exit 1 }

Write-Host "Attente de la console..."
for ($i = 0; $i -lt 120; $i++) {
  try { Invoke-WebRequest -UseBasicParsing -Uri "http://127.0.0.1:9090/" -TimeoutSec 2 | Out-Null; break } catch { Start-Sleep -Seconds 2 }
}
Start-Process "http://127.0.0.1:9090/"
Write-Host "Console prête : http://127.0.0.1:9090 (identifiant admin)" -ForegroundColor Green
Read-Host "Entrée pour fermer"
