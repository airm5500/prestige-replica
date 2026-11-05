# Prestige Replica Manager : démarrer en 4 étapes (Windows, sans Docker)

Objectif de ce premier essai : **voir le serveur MariaDB de la pharmacie dans la console**, en lecture seule. Rien n'est modifié sur MariaDB ni sur Prestige. Les sauvegardes, la réplication et les bascules viendront après, une fois que ce premier essai fonctionne.

Tout se fait sur **le PC où tourne MariaDB** (le serveur Prestige, ou un PC de test qui a MariaDB). Rien d'autre à installer : Java et la petite base de données de la console sont inclus dans le kit.

## Préparation

1. Décompresser le kit dans un dossier, par exemple `C:\PrestigeReplica`.
2. Ouvrir **PowerShell en administrateur** : menu Démarrer, taper « PowerShell », clic droit, **Exécuter en tant qu'administrateur**.
3. Taper :
   ```powershell
   Set-ExecutionPolicy -Scope Process Bypass -Force
   cd C:\PrestigeReplica
   ```
   Garder cette fenêtre ouverte pour les étapes 1 et 4.

## Étape 1 : installer la console (3 minutes)

```powershell
.\1-installer-console.ps1
```

Le script installe deux services Windows qui démarrent tout seuls avec le PC :
- **PrestigeReplicaDB** : base interne de la console, sur le port 5433, accessible depuis ce PC uniquement ;
- **PrestigeReplicaConsole** : la console, sur **http://127.0.0.1:9090**.

À la fin, le navigateur s'ouvre et le mot de passe `admin` est affiché. Il est aussi enregistré dans `MOT-DE-PASSE-ADMIN.txt`.

> Si un port est déjà pris : `.\1-installer-console.ps1 -Port 9091 -PortBase 5434`.

## Étape 2 : créer le compte de supervision dans MariaDB (2 minutes)

Ouvrir **`2-compte-supervision.sql`**, remplacer le mot de passe par le vôtre, puis l'exécuter dans HeidiSQL (ou `mysql -u root -p`) sur le serveur MariaDB.

Ce compte ne voit **aucune donnée** des pharmacies : seulement l'état du serveur et de la réplication.

## Étape 3 : déclarer la pharmacie et le serveur dans la console (5 minutes)

Se connecter à http://127.0.0.1:9090 avec `admin`, puis menu **Paramètres**. Remplir les cartes dans l'ordre :

1. **Organisation** : code `PRESTIGE`, nom de votre société → Créer.
2. **Pharmacie** : choisir l'organisation, code court (ex. `APL`), nom (ex. `Pharmacie Appaul`) → Créer.
3. **Topologie** : choisir la pharmacie, nom `prestige-prod`, mode A → Créer.
4. **Serveur MariaDB** : choisir la pharmacie et la topologie, nom (ex. `srv-prestige`), rôle **Primaire**, adresse `127.0.0.1:3306` → **Enregistrer et obtenir le jeton**.
5. **Copier le jeton** affiché (il commence par `prm_`). Il ne sera plus jamais réaffiché.

## Étape 4 : installer l'agent (2 minutes)

Dans la même fenêtre PowerShell, en remplaçant les deux valeurs :

```powershell
.\3-installer-agent.ps1 -Jeton "prm_xxxxxxxx" -MotDePasseMariaDB "le mot de passe de l'étape 2"
```

Si MariaDB n'écoute pas sur le port 3306, ajouter par exemple `-AdresseMariaDB "127.0.0.1:3307"`.

Le script vérifie la connexion à MariaDB, installe l'agent comme service Windows (**PrestigeReplicaAgent**) et le démarre.

**Résultat** : dans la console, page **Parc**, le serveur apparaît en moins d'une minute, avec sa version, son état et l'espace disque.

## Et ensuite ?

Une fois ce premier essai réussi, envoyez une capture de la page Parc. On activera ensuite, une par une :

1. le journal binaire de MariaDB (préalable à toute réplication ; demande un redémarrage de MariaDB) ;
2. les sauvegardes chiffrées ;
3. la réplique ;
4. le routeur et les bascules.

## Récupérer les bases d'un PC mort (disque branché sur un autre PC)

Si le PC MariaDB ne démarre plus mais que son disque est lisible : brancher le disque sur un autre PC, puis, dans PowerShell en administrateur, dans ce dossier :

```powershell
.\4-recuperer-disque.ps1 -Source "E:\Program Files\MariaDB 10.6\data"
```

(remplacer `E:` et `10.6` par ce que vous voyez dans l'Explorateur). Le script ne modifie jamais le disque d'origine, travaille sur une copie dans `C:\Recuperation\<date>`, essaie toutes les méthodes de récupération l'une après l'autre et ouvre un rapport en français. À la fin, `resultat\restauration-complete.sql` se restaure sur le nouveau serveur : `mariadb -u root -p < restauration-complete.sql`. Détails : `docs/recuperation-fichiers.md`.

## En cas de problème

- **La console ne s'ouvre pas** : services Windows (`services.msc`), vérifier que *Prestige Replica Manager - Console* est démarré. Le journal se trouve dans `C:\ProgramData\PrestigeReplicaManager\console\logs`.
- **`MariaDB joignable : false` à l'étape 4** : le mot de passe ou le port est faux, ou le compte de l'étape 2 n'a pas été créé.
- **Pour tout autre message rouge** : faire une capture d'écran et l'envoyer.
- **Tout retirer** : `.\desinstaller-console.ps1`.

---
*Construction du kit (pour les développeurs)* : le dossier `console\` du kit contient `app\` (`control-plane/target/quarkus-app`), `runtime\` (JRE Temurin 21 Windows x64), `pgsql\` (binaires PostgreSQL 16 Windows x64, avec `vcruntime140*.dll` et `msvcp140.dll` repris du JRE) et `service\PrestigeReplicaConsole.exe` (WinSW 2.12 x64). `prm-agent.exe` : `GOOS=windows GOARCH=amd64 go build ./cmd/prm-agent`.
