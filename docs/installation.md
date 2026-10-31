# Installation (Lots 1 à 4, archivage des journaux)

## 1. Compte de supervision MariaDB

Sur **chaque** serveur MariaDB, un compte local en lecture seule, sans aucun droit sur les données :

```sql
-- MariaDB 10.5.9 et plus récent
CREATE USER 'prm_monitor'@'localhost' IDENTIFIED BY '<mot-de-passe-long>';
GRANT BINLOG MONITOR, REPLICA MONITOR, PROCESS, SHOW DATABASES ON *.* TO 'prm_monitor'@'localhost';

-- MariaDB 10.3 / 10.4
GRANT REPLICATION CLIENT, PROCESS, SHOW DATABASES ON *.* TO 'prm_monitor'@'localhost';
```

`SHOW DATABASES` est facultatif (liste des bases dans la fiche nœud). L'agent ouvre en plus sa session en `TRANSACTION READ ONLY`.

## 2. Control Plane (Linux, service systemd)

1. Créer l'utilisateur système : `useradd --system --no-create-home --shell /usr/sbin/nologin prm`.
2. Copier `control-plane/target/quarkus-app/` dans `/opt/prestige-replica-manager/app/`.
3. Fournir un runtime Java 21 privé dans `/opt/prestige-replica-manager/runtime/` (en attendant le paquet `jlink`/`jpackage`, une copie d'un JRE 21 convient).
4. Copier `deploy/systemd/control-plane.env.example` vers `/etc/prestige-replica-manager/control-plane.env` (mode `0600`, propriétaire `prm`) et le compléter. Le mot de passe d'amorçage n'est utilisé qu'au premier démarrage : retirez-le ensuite.
5. `cp deploy/systemd/prestige-replica-manager.service /etc/systemd/system/ && systemctl enable --now prestige-replica-manager`.
6. Vérifier : `curl -k https://127.0.0.1:8443/health/ready`.

Par défaut la console n'écoute que sur `127.0.0.1`. Pour un accès réseau, définir `PRM_HTTP_HOST`, un certificat TLS (`PRM_TLS_CERT`/`PRM_TLS_KEY`) et `PRM_INSECURE_REQUESTS=disabled`.

**TLS mutuel des agents** : `PRM_TLS_CLIENT_AUTH=REQUEST`, `PRM_TLS_CLIENT_CA=<truststore des certificats d'agent>` puis `PRM_AGENT_REQUIRE_CLIENT_CERT=true`. Le jeton reste exigé en plus du certificat.

Conteneurs : `deploy/compose/compose.yaml` (PostgreSQL + Control Plane ; ajouter un reverse proxy TLS).

## 3. Agent

### Linux

```bash
useradd --system --no-create-home --shell /usr/sbin/nologin prm-agent
install -m 0755 prm-agent-linux-amd64 /usr/local/bin/prm-agent
install -d -m 0750 -o root -g prm-agent /etc/prestige-replica-manager
cp deploy/systemd/agent.example.json /etc/prestige-replica-manager/agent.json   # à adapter
printf '%s' '<jeton affiché par la console>' > /etc/prestige-replica-manager/agent.token
printf '%s' '<mot de passe prm_monitor>'     > /etc/prestige-replica-manager/mariadb.password
chown root:prm-agent /etc/prestige-replica-manager/* && chmod 0640 /etc/prestige-replica-manager/*
prm-agent check -config /etc/prestige-replica-manager/agent.json
cp deploy/systemd/prestige-replica-agent.service /etc/systemd/system/
systemctl enable --now prestige-replica-agent
```

### Windows

En PowerShell administrateur : `deploy/windows/install-agent.ps1`, puis placer `agent.json`, `agent.token` et `mariadb.password` dans `C:\ProgramData\PrestigeReplicaManager\` et `Start-Service PrestigeReplicaAgent`.

### Comportement réseau

L'agent initie une connexion HTTPS sortante vers la console ; aucun port entrant n'est ouvert dans la pharmacie. Si la console est injoignable, les rapports sont conservés en mémoire (24 h par défaut à 30 s d'intervalle) et renvoyés au retour de la liaison. La CLI `prm-agent status` reste utilisable sans console.

## 4. Seuils par défaut

| Alerte | Seuil |
|---|---|
| Retard de réplication | avertissement 60 s, critique 300 s (hors délai volontaire d'une réplique différée) |
| Disque libre | avertissement 20 %, critique 10 % |
| Agent silencieux | 90 s (la console évalue toutes les 30 s) |

Ils se règlent par topologie. Une topologie en **maintenance** n'ouvre ni ne notifie d'incident.

## 5. Sauvegardes et construction de réplique (Lot 2)

Ces fonctions sont **désactivées par défaut** sur chaque agent : la console ne peut demander que ce que le fichier `agent.json` du serveur autorise (`backup.enabled`, `restoreTest.enabled`, `provisioning.enabled`). Exemple Windows complet : [`deploy/windows/agent.example.json`](../deploy/windows/agent.example.json).

### 5.1 Activer le journal binaire (préalable à toute réplication)

Sur le serveur mesuré, `Binlog_bytes_written = 0` : le journal binaire est désactivé. Dans la console, bouton **Vérifier les prérequis** sur la fiche du serveur : il indique ce qui manque et propose la section `[mysqld]` à coller. Sous Windows :

1. Ouvrir `C:\Program Files\MariaDB 10.6\data\my.ini` (garder une copie du fichier d'origine).
2. Ajouter dans `[mysqld]` les lignes proposées (au minimum `server_id`, `log_bin=mariadb-bin`, `binlog_format=ROW`, `log_slave_updates=ON`, `gtid_strict_mode=ON`, `binlog_expire_logs_seconds=604800`).
3. Redémarrer pendant une fenêtre calme : `net stop MariaDB` puis `net start MariaDB` (PowerShell administrateur). Prévoir quelques dizaines de secondes de coupure de Prestige.
4. Relancer **Vérifier les prérequis** : `log_bin` doit être OK.

Chaque serveur doit avoir un `server_id` unique ; la console en suggère un libre pour chaque réplique.

### 5.2 Comptes MariaDB

À créer **sur le primaire** (ils sont recopiés sur les répliques par la sauvegarde et la réplication) :

```sql
-- Sauvegarde (mariadb-backup)
CREATE USER 'prm_backup'@'localhost' IDENTIFIED BY '<mot-de-passe-long>';
CREATE USER 'prm_backup'@'127.0.0.1' IDENTIFIED BY '<mot-de-passe-long>';
GRANT RELOAD, PROCESS, LOCK TABLES, BINLOG MONITOR ON *.* TO 'prm_backup'@'localhost', 'prm_backup'@'127.0.0.1';

-- Flux de réplication (utilisé par les répliques pour se connecter au primaire)
CREATE USER 'prm_repl'@'%' IDENTIFIED BY '<mot-de-passe-long>';   -- restreindre l'hôte aux adresses des répliques
GRANT REPLICATION SLAVE ON *.* TO 'prm_repl'@'%';
-- Archivage continu des journaux (§9) : liste des binlogs du primaire
GRANT BINLOG MONITOR ON *.* TO 'prm_repl'@'%';

-- Pilotage local de la réplique (uniquement depuis la machine elle-même)
CREATE USER 'prm_admin'@'127.0.0.1' IDENTIFIED BY '<mot-de-passe-long>';
-- MariaDB 10.6 : SUPER est nécessaire pour SET GLOBAL read_only
GRANT RELOAD, SUPER, REPLICATION SLAVE ADMIN, SLAVE MONITOR, REPLICATION MASTER ADMIN ON *.* TO 'prm_admin'@'127.0.0.1';
-- MariaDB 10.11 et plus : remplacer SUPER par READ_ONLY ADMIN, CONNECTION ADMIN
```

Aucun de ces comptes n'a de droit sur les tables de Prestige (`SELECT`, `INSERT`…).

### 5.3 Clé de chiffrement des sauvegardes

Les sauvegardes sont chiffrées (AES-256-GCM) avant d'être écrites sur le mini-serveur. Générer **une** clé par pharmacie et la copier sur chaque agent qui sauvegarde, restaure ou reconstruit :

```powershell
& "C:\Program Files\PrestigeReplicaManager\prm-agent.exe" keygen -out C:\ProgramData\PrestigeReplicaManager\backup.key
```

Conserver une copie de ce fichier **hors de la pharmacie** (coffre de mots de passe, clé USB au coffre). Sans elle, aucune sauvegarde n'est restaurable. Son identifiant court (affiché par `keygen` et dans les prérequis) figure dans chaque manifeste.

### 5.4 Stockage

`backup.root` désigne le dossier de sauvegarde : un partage du mini-serveur (`\\MINI-SERVEUR\sauvegardes-prestige`) ou un disque local. Le compte du service de l'agent doit pouvoir y écrire ; pour un partage réseau, faire tourner le service sous un compte de domaine ou local dédié plutôt que `LocalSystem`. Prévoir 1,2 fois la taille du datadir par sauvegarde conservée (environ 25 Go pour appaul + capitale, avant compression).

### 5.5 Construction d'une réplique

Sur la machine qui deviendra réplique : MariaDB 10.6 installé (même version que le primaire), service `MariaDB` arrêtable par l'agent, `provisioning.enabled: true`, `restoreTest.enabled: true`, accès au même `backup.root` et à la même clé. Ensuite dans la console : **Sauvegardes** → sauvegarder → tester la restauration → **Construire une réplique…** ; une seconde personne (rôle Approbateur) valide en ressaisissant le code de la pharmacie. L'ancien dossier de données est conservé à côté (`data.prm-old-<date>`) ; supprimez-le une fois la réplique validée.

### 5.6 CLI de secours (sans console)

```text
prm-agent preflight    -config agent.json -role REPLICA
prm-agent backup       -config agent.json            → backup.root\local\<horodatage>
prm-agent restore-test -config agent.json -dir local\<horodatage>
```

## 6. Pause, reprise et bascule planifiée (Lot 3)

Activer `replicationControl.enabled: true` dans l'`agent.json` de **chaque** serveur de la topologie (primaire compris) : sans cette autorisation locale, la console ne peut ni suspendre, ni basculer, ni rattacher ce serveur. Les comptes `prm_admin` et `prm_repl` du §5.2 sont réutilisés (par défaut ceux de `provisioning`).

```json
"replicationControl": {
  "enabled": true,
  "adminUser": "prm_admin",
  "adminPasswordFile": "C:\\ProgramData\\PrestigeReplicaManager\\admin.password",
  "replicationUser": "prm_repl",
  "replicationPasswordFile": "C:\\ProgramData\\PrestigeReplicaManager\\repl.password",
  "protectedUsers": []
}
```

Points à vérifier avant la première bascule :

1. **Le compte de Prestige ne doit pas avoir le privilège `SUPER`** (ni `READ_ONLY ADMIN` en 10.11). Sinon `read_only` ne bloque pas ses écritures : l'agent le détecte (les positions GTID continuent d'avancer), annule la bascule et rouvre l'ancien primaire. Vérification : `SHOW GRANTS FOR 'prestige'@'%';`.
2. Chaque nœud doit avoir son **adresse hôte:port** renseignée dans la console : c'est l'adresse que prendront les autres répliques, et que Prestige devra utiliser après la bascule.
3. Après une bascule réussie, si Prestige ne passe pas par le **routeur de connexion** (§7), le faire pointer vers le nouveau primaire (l'adresse est rappelée dans le rapport).
4. `protectedUsers` : comptes techniques dont les connexions ne doivent pas être coupées lors de la rétrogradation (outil de supervision tiers, par exemple). Les comptes de l'agent le sont toujours.

L'agent consulte la console toutes les 5 s (`jobPollSeconds`), et chaque seconde pendant une bascule.

## 7. Routeur de connexion (Lot 4)

Le routeur donne à Prestige une **adresse fixe** : après une bascule, plus aucun poste n'est à reconfigurer. C'est un relais TCP intégré à `prm-agent` qui interroge chaque serveur toutes les secondes et dirige les connexions vers **le seul serveur accessible en écriture et qui ne réplique pas**. Dans tous les autres cas (bascule en cours, primaire perdu, deux serveurs en écriture), il **refuse** les connexions au lieu de deviner : Prestige affiche une erreur de connexion pendant quelques secondes, mais jamais deux bases ne reçoivent de ventes en même temps. Lorsqu'il change de serveur, il coupe les connexions encore ouvertes vers l'ancien.

Où l'installer :

- **sur chaque poste Prestige** (recommandé, pas de point unique de panne) : service autonome `deploy/windows/install-router.ps1` avec `router.json` (modèle `deploy/windows/router.example.json`), puis dans Prestige, serveur `127.0.0.1`, port `3310` ;
- ou sur un serveur d'accès unique, en écoutant sur son adresse réseau (`"listen": "192.168.1.20:3310"`) ; ce serveur devient alors indispensable ;
- ou dans l'agent d'un serveur (section `router` de `agent.json`) : son état (serveur choisi, connexions refusées, situations à deux primaires évitées) s'affiche alors sur la fiche du serveur dans la console.

```json
"router": {
  "enabled": true,
  "listen": "127.0.0.1:3310",
  "targets": ["192.168.1.10:3306", "192.168.1.11:3306"],
  "user": "prm_monitor",
  "passwordFile": "C:\\ProgramData\\PrestigeReplicaManager\\mariadb.password",
  "probeIntervalMs": 1000,
  "failuresBeforeDown": 2
}
```

Points à vérifier :

1. Le compte `prm_monitor` doit pouvoir se connecter **depuis la machine du routeur** sur chaque serveur cible (`CREATE USER 'prm_monitor'@'192.168.1.%' ...` avec les mêmes droits qu'au §1).
2. Vu de MariaDB, les connexions de Prestige viennent de la machine du routeur : le compte de Prestige doit être autorisé depuis cette adresse (`127.0.0.1` si le routeur est sur le même serveur que MariaDB).
3. Le chiffrement TLS éventuel entre Prestige et MariaDB traverse le routeur sans modification.
4. `failuresBeforeDown` : nombre de sondes manquées avant de considérer un serveur perdu (2 par défaut, soit environ 2 s). Une seule sonde perdue ne coupe rien.

## 8. Bascule d'urgence et isolement (Lot 4)

Rien de plus à activer que `replicationControl` (§6) : la bascule d'urgence réutilise les mêmes autorisations locales. Elle n'est **jamais automatique** : une personne la demande, une autre l'approuve. Procédure complète, avec le cas « console injoignable » : [procedure-urgence.md](procedure-urgence.md).

Pour que l'agent puisse **arrêter MariaDB et désactiver son démarrage automatique** sur un ancien primaire (et, au Lot 2, reconstruire une réplique), son compte de service doit avoir des droits sur le service MariaDB. En PowerShell administrateur, sur chaque serveur :

```powershell
deploy\windows\grant-service-control.ps1 -MariaDbService MariaDB
```

L'agent d'un serveur isolé le maintient en lecture seule (contrôle toutes les 2 s, marqueur dans le dossier de `agent.json`, ou `stateDir`), y compris après un redémarrage et sans console.

Recommandé : `read_only=ON` dans le `my.ini` de **toutes** les répliques. Un ancien primaire qui redémarre seul revient ainsi en lecture seule. S'il revient malgré tout en écriture, le routeur refuse les connexions (deux primaires), la console le remet en lecture seule au rapport suivant de son agent et ouvre un incident critique « isolé de nouveau accessible en écriture ».

### Exercices de panne

Bascule → onglet **Exercice** : bascule planifiée vers une réplique puis retour automatique, programmable la nuit, avec double validation. À placer à distance de l'heure des sauvegardes (une sauvegarde en cours fait échouer les contrôles, sans effet). Le rappel par défaut est de 90 jours sans exercice réussi ; il se règle sur le même écran (0 = jamais).


## 9. Archivage des journaux et restauration à un instant précis

Les sauvegardes complètes sont quotidiennes. Pour revenir **à la seconde près** (ventes supprimées par erreur à 14 h 32, mauvaise mise à jour de masse), un agent recopie en continu les journaux binaires du primaire, chiffrés, à côté des sauvegardes. La restauration se fait **dans une instance isolée**, jamais sur la production : on en extrait les tables utiles.

1. Sur le serveur qui sauvegarde (en général le mini-serveur), dans `agent.json` :
   ```json
   "binlogArchive": { "enabled": true }
   ```
   Il utilise par défaut le compte de réplication (`prm_repl`), qui doit avoir `BINLOG MONITOR` en plus de `REPLICATION SLAVE` (§5.2). Il faut aussi `backup.root` et la clé de chiffrement. L'agent lit les journaux à l'adresse du primaire déclarée dans l'inventaire ; il suit les bascules tout seul.
2. Console → **Sauvegardes** → **Régler l'archivage** : activer, choisir ce serveur, intervalle (5 minutes par défaut ; c'est la perte maximale si tous les serveurs étaient perdus ensemble).
3. Côté primaire, garder les binlogs au moins un jour : `binlog_expire_logs_seconds=604800` (7 jours) est recommandé dans `my.ini`. Si le primaire purge un journal avant son archivage, la console ouvre un incident critique « trou dans l'archive » jusqu'à la prochaine sauvegarde complète.
4. La restauration (**Restaurer à un instant…**) s'exécute sur le serveur de restauration de contrôle (`restoreTest.enabled`), à partir de la dernière sauvegarde **vérifiée** antérieure à l'instant visé. Les tables demandées sont exportées, chiffrées, dans `backup.root\tenants\…\recoveries\…`. Pour les lire :
   ```powershell
   prm-agent decrypt -config agent.json -in "<fichier>.sql.prmseal" -out vente.sql
   ```
   Importer le fichier dans une base de travail, **jamais directement dans la production**, puis recopier les seules lignes à récupérer.

Les journaux archivés antérieurs à la plus ancienne sauvegarde conservée sont supprimés automatiquement.

La page **Journaux** cherche dans cette archive (qui a supprimé ou modifié quoi, et quand) avec le même agent : rien de plus à installer. Garder `binlog_annotate_row_events=ON` (valeur par défaut) sur le primaire pour voir la requête d'origine de chaque changement.
