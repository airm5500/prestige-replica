# Prestige Replica Manager : démarrer en 4 étapes

Objectif de ce premier essai : **voir le serveur MariaDB de la pharmacie dans la console**, en lecture seule. Rien n'est modifié sur MariaDB ni sur Prestige. Les sauvegardes, la réplication et les bascules viendront après, une fois que ce premier essai fonctionne.

Tout se fait sur **le PC où tourne MariaDB** (le serveur Prestige, ou un PC de test qui a MariaDB).

## Étape 1 : démarrer la console (20 minutes, une seule fois)

1. Installer **Docker Desktop** : https://www.docker.com/products/docker-desktop/ puis le lancer (icône baleine) et attendre qu'il indique qu'il est prêt.
2. Décompresser ce kit dans un dossier, par exemple `C:\PrestigeReplica`.
3. Clic droit sur **`1-demarrer-console.ps1`** > **Exécuter avec PowerShell**.
   - La première fois, la construction dure 10 à 15 minutes.
   - Le mot de passe de la console est affiché et enregistré dans `MOT-DE-PASSE-ADMIN.txt`.
4. Le navigateur s'ouvre sur **http://127.0.0.1:9090**. Se connecter avec `admin` et ce mot de passe.

> Si Windows refuse d'exécuter le script : ouvrir PowerShell et taper `Set-ExecutionPolicy -Scope CurrentUser RemoteSigned`, répondre `O`, puis recommencer.

## Étape 2 : créer le compte de supervision dans MariaDB (2 minutes)

Ouvrir **`2-compte-supervision.sql`**, remplacer le mot de passe par le vôtre, puis l'exécuter dans HeidiSQL (ou `mysql -u root -p`) sur le serveur MariaDB.

Ce compte ne voit **aucune donnée** des pharmacies : seulement l'état du serveur et de la réplication.

## Étape 3 : déclarer la pharmacie et le serveur dans la console (5 minutes)

Dans la console, menu **Paramètres**, remplir les cartes dans l'ordre, de haut en bas :

1. **Organisation** : code `PRESTIGE`, nom de votre société → Créer.
2. **Pharmacie** : choisir l'organisation, code court (ex. `APL`), nom (ex. `Pharmacie Appaul`) → Créer.
3. **Topologie** : choisir la pharmacie, nom `prestige-prod`, mode A → Créer.
4. **Serveur MariaDB** : choisir la pharmacie et la topologie, nom (ex. `srv-prestige`), rôle **Primaire**, adresse `127.0.0.1:3306` → **Enregistrer et obtenir le jeton**.
5. **Copier le jeton** affiché (il commence par `prm_`). Il ne sera plus jamais réaffiché.

## Étape 4 : installer l'agent (5 minutes)

1. Ouvrir **PowerShell en administrateur** (clic droit sur PowerShell > Exécuter en tant qu'administrateur).
2. Aller dans le dossier du kit : `cd C:\PrestigeReplica`
3. Lancer, en remplaçant les deux valeurs :
   ```powershell
   .\3-installer-agent.ps1 -Jeton "prm_xxxxxxxx" -MotDePasseMariaDB "le mot de passe de l'étape 2"
   ```
   - Si MariaDB n'écoute pas sur le port 3306, ajouter `-AdresseMariaDB "127.0.0.1:3307"` (par exemple).
4. Le script vérifie la connexion à MariaDB, installe l'agent comme service Windows et le démarre.

**Résultat** : dans la console, page **Parc**, le serveur apparaît en moins d'une minute, avec sa version, son état et l'espace disque.

## Et ensuite ?

Une fois ce premier essai réussi, envoyez une capture de la page Parc. On activera ensuite, une par une :

1. le journal binaire de MariaDB (préalable à toute réplication ; demande un redémarrage de MariaDB) ;
2. les sauvegardes chiffrées ;
3. la réplique ;
4. le routeur et les bascules.

## En cas de problème

- La console ne s'ouvre pas : vérifier que Docker Desktop est démarré, puis relancer `1-demarrer-console.ps1`.
- `MariaDB joignable : false` à l'étape 4 : le mot de passe ou le port est faux, ou le compte de l'étape 2 n'a pas été créé.
- Pour tout autre message rouge : faire une capture d'écran et l'envoyer.
