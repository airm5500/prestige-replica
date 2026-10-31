# Installation (Lots 1 et 2)

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

-- Pilotage local de la réplique (uniquement depuis la machine elle-même)
CREATE USER 'prm_admin'@'127.0.0.1' IDENTIFIED BY '<mot-de-passe-long>';
-- MariaDB 10.6 : SUPER est nécessaire pour SET GLOBAL read_only
GRANT RELOAD, SUPER, REPLICATION SLAVE ADMIN, SLAVE MONITOR, REPLICATION MASTER ADMIN ON *.* TO 'prm_admin'@'127.0.0.1';
-- MariaDB 10.11 et plus : remplacer SUPER par READ_ONLY ADMIN
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
