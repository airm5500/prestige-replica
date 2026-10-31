# Installation (Lot 1)

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
