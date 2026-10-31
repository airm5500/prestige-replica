# Prestige Replica Manager

Application autonome pour **superviser, puis à terme déployer, réparer et auditer** la réplication MariaDB des bases Prestige installées dans plusieurs pharmacies.

L'application pilote les mécanismes natifs de MariaDB (binlog, GTID, `mariadb-backup`) ; elle ne réimplémente jamais la réplication. Si la console s'arrête, la réplication MariaDB continue.

> **État : Lot 1 — observation en lecture seule.** Aucune commande de cette version ne modifie MariaDB. Voir [docs/perimetre-lot1.md](docs/perimetre-lot1.md) pour ce qui est livré, ce qui ne l'est pas encore et les décisions attendues.

![Accueil du parc](docs/images/parc.png)

## Composants

| Dossier | Rôle | Technologie |
|---|---|---|
| `agent/` | Agent installé près de chaque MariaDB : collecte l'état (threads IO/SQL, retard, GTID, binlogs, configuration, disque) et l'envoie au Control Plane. Liste blanche fermée d'instructions `SHOW`, aucun port ouvert, file bornée hors connexion, CLI de secours `prm-agent status`. | Go, binaire unique Linux/Windows |
| `control-plane/` | API REST, inventaire multi-pharmacies, règles d'alerte, incidents, notifications courriel/webhook, audit append-only chaîné, sondes `/health/*`. | Java 21, Quarkus, PostgreSQL, Flyway |
| `web/` | Console : parc, fiche nœud, incidents, audit, paramètres. Pharmacie active rappelée en permanence. | Vue 3, TypeScript strict, Vite, Pinia |
| `deploy/` | Unités `systemd`, installation du service Windows, `docker compose`. | |
| `scripts/lab/` | Laboratoire local primaire → réplique GTID pour les essais. | |

## Démarrage rapide (laboratoire)

Prérequis : Go 1.24+, JDK 21, Maven, Node 22, PostgreSQL, MariaDB 10.5+.

```bash
# 1. MariaDB primaire (3307) + réplique GTID (3308)
scripts/lab/start-lab.sh

# 2. Base de métadonnées
createuser prm --pwprompt && createdb -O prm prm

# 3. Construction complète (interface intégrée au Control Plane + agents)
SKIP_TESTS=1 scripts/build-all.sh

# 4. Control Plane
export PRM_DB_PASSWORD=... PRM_SESSION_KEY=$(openssl rand -hex 24) PRM_BOOTSTRAP_ADMIN_PASSWORD='un-mot-de-passe-long'
java -jar control-plane/target/quarkus-app/quarkus-run.jar
# → http://127.0.0.1:8080 (compte « admin »)
```

Dans la console : **Paramètres** → créer l'organisation, la pharmacie, la topologie, puis enregistrer chaque serveur MariaDB. Le jeton affiché une seule fois va dans le fichier `agentTokenFile` de l'agent :

```bash
PRM_DB_PW=monitor-lab dist/prm-agent-linux-amd64 run -config agent.json
```

Installation en production : [docs/installation.md](docs/installation.md).

## Tests

```bash
cd agent && go test ./...                      # agent
cd control-plane && mvn test                   # règles d'alerte + parcours API complet (PostgreSQL requis : prm_test)
cd web && npm run build                        # vérification TypeScript stricte
```

## API

OpenAPI : `/q/openapi`. Principales routes du Lot 1 :

```text
POST /api/v1/agent/reports              rapports d'agent (jeton Bearer, mTLS optionnel)
GET  /api/v1/fleet                      état du parc par pharmacie / topologie / nœud
POST /api/v1/nodes/register             inventaire manuel d'un nœud, jeton d'agent rendu une fois
POST /api/v1/nodes/{id}/rotate-token    révocation et remplacement du jeton
GET  /api/v1/nodes/{id}  …/health  …/metrics?hours=
GET  /api/v1/incidents   POST /api/v1/incidents/{id}/acknowledge
GET  /api/v1/audit  …/export  …/verify
GET/POST /api/v1/tenants  /sites  /topologies  /users  /notification-channels
GET  /health/live  /health/ready  /health/startup   /q/metrics
```
