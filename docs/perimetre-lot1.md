# Périmètre livré — Lot 1 « Observation sans risque »

Référence : spécification *Proposition d'application de réplication MariaDB*, §12 (Lot 1), §15 (recommandation de démarrage) et §21 (phase 1).

## Livré

**Agent Go (lecture seule)**
- Collecte : version, `server_id`, `read_only`, binlog (activation, format, image de ligne, rétention), GTID (`current`, `binlog`, `slave` pos, mode strict), semi-synchrone, fuseaux horaires, TLS, connexions ; `SHOW ALL SLAVES STATUS` complet (threads IO/SQL, retard, délai volontaire, erreurs, GTID IO, relay log, filtres) ; liste et volume des binlogs ; espace disque du datadir et du dossier des binlogs.
- Liste blanche fermée de six instructions (`SHOW …` et `SET SESSION TRANSACTION READ ONLY`), vérifiée par un test. Aucune commande reçue de la console.
- Masquage des secrets dans les messages d'erreur ; mot de passe lu depuis un fichier ou l'environnement, jamais depuis le JSON de configuration.
- Envoi HTTPS sortant, mTLS optionnel, file bornée hors connexion avec reprise et délai progressif.
- CLI de secours : `status`, `status -json`, `check`, `version`.
- Service `systemd` et service Windows natif (SCM).

**Control Plane Quarkus**
- Modèle tenant → site (pharmacie) → topologie → nœud ; `tenant_id`/`site_id` sur toutes les tables ; contrôle de périmètre côté serveur (404 hors périmètre).
- Jeton d'agent propre à chaque nœud, stocké en SHA-256, affiché une seule fois, rotation avec révocation immédiate.
- Règles d'alerte : agent silencieux, MariaDB injoignable, threads IO/SQL arrêtés, erreurs IO/SQL, binlogs purgés avant consommation (erreur 1236), retard (avec soustraction du `MASTER_DELAY`), disque, binlog désactivé sur le primaire, réplique accessible en écriture, primaire en lecture seule, réplique sans canal, GTID non utilisé, filtres de réplication actifs, `server_id` dupliqué dans une topologie, rétention de binlog < 24 h, format non `ROW`, réplique différée sans délai.
- Incidents : niveau, début, dernière observation, accusé de réception, responsable, résolution automatique ; un seul incident ouvert par condition ; fenêtres de maintenance.
- Notifications courriel (SMTP local) et webhook (passerelle SMS/WhatsApp) à l'ouverture, à l'aggravation et à la résolution.
- Audit append-only : empreintes SHA-256 chaînées, déclencheurs PostgreSQL interdisant `UPDATE`/`DELETE`/`TRUNCATE`, vérification d'intégrité, export CSV.
- Rôles §19.4 : super-administrateur, exploitant, approbateur, administrateur de pharmacie, auditeur, lecture seule.
- Sondes `/health/live|ready|startup`, métriques Prometheus `/q/metrics`, OpenAPI.
- Rétention des métriques (30 jours par défaut).

**Console Vue 3** : accueil du parc (état, RPO estimé), fiche nœud (canaux, erreurs exactes, configuration, historique), incidents, audit, paramètres/inventaire. Sélecteur de pharmacie et bandeau de la pharmacie active.

## Vérifié

- 10 tests unitaires des règles d'alerte, 7 tests d'intégration API sur PostgreSQL réel (dont isolation entre deux tenants, immutabilité de l'audit, révocation de jeton), tests de l'agent.
- Bout en bout sur MariaDB 10.11 (primaire → réplique GTID) :
  - arrêt du thread SQL : incident critique ouvert au rapport suivant (< intervalle de collecte), résolu automatiquement au redémarrage ;
  - conflit de clé réel (écriture locale sur la réplique) : erreur 1062 exacte affichée, sans secret ;
  - console arrêtée ~1 min : les agents ont conservé puis renvoyé leurs rapports, sans trou dans l'historique ;
  - agent arrêté : incident « agent silencieux » ouvert ~45 s après le dernier contact (seuil 30 s) ;
  - aucun mot de passe dans les journaux ni dans la base.

Critère MVP « arrêt du thread SQL → alerte en moins de deux minutes » : respecté avec l'intervalle par défaut de 30 s.

## Écarts assumés et reste à faire

| Sujet | État | Lot prévu |
|---|---|---|
| MFA, SSO OIDC/SAML | non livré — authentification par mot de passe (bcrypt) + session chiffrée | Lot 1 bis |
| Coffre de secrets (Vault) | non nécessaire au Lot 1 : aucun secret MariaDB n'est stocké par la console | Lot 2 |
| Émission des certificats d'agent (PKI courte durée) | mTLS supporté, certificats à fournir | Lot 2 |
| Paquets `jlink`/`jpackage` (MSI, DEB, RPM), signature, SBOM | non livré — JAR + unités systemd + script Windows + compose | Lot 2 |
| Sauvegardes `mariadb-backup`, restauration de contrôle, provisionnement de réplique | non livré | Lot 2 |
| Pause/reprise, reconstruction, switchover, approbations | non livré | Lot 3 |
| Fencing, failover, proxy | non livré | Lot 4 |
| Explorateur de binlogs, PITR, récupération sélective, flashback | non livré | Phase 2 |
| Analyse des requêtes, tableaux InnoDB | non livré | §16.10 |
| Server-Sent Events | rafraîchissement périodique (10–15 s) à la place | — |
| PWA | non livrée | — |

## Décisions attendues (§14)

Ces réponses orientent les lots suivants : systèmes et versions MariaDB réellement en service chez les clients, RPO/RTO visés, type de liaison réseau (IP fixe, VPN, sortant seulement), console centrale unique ou par pharmacie, emplacement et durée de conservation des sauvegardes.
