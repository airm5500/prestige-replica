# Périmètre livré : archivage des journaux et restauration à un instant précis

La sauvegarde quotidienne permet de revenir à l'état de la nuit. Cette étape permet de revenir **à la seconde près**, par exemple juste avant une suppression accidentelle, sans jamais toucher la production.

![Restaurer à un instant](images/restauration-instant.png)

## Livré

- **Archivage continu des journaux binaires** (opération `BINLOG_ARCHIVE`, activable dans `agent.json` par `binlogArchive.enabled`) :
  - l'agent du serveur de sauvegarde lit les binlogs du primaire comme une réplique (compte `prm_repl`), toutes les N minutes (5 par défaut, réglable de 1 à 60) ;
  - il les chiffre (même clé que les sauvegardes) et les range sous `backup.root`. Le fichier en cours d'écriture est recopié à chaque passage, ce qui borne la perte à l'intervalle choisi ;
  - le serveur MariaDB lu est contrôlé (`server_id`) ;
  - l'archivage suit le primaire après une bascule ;
  - les journaux antérieurs à la plus ancienne sauvegarde conservée sont supprimés automatiquement.
- **Catalogue dans la console** (page Sauvegardes) : couverture (de quand à quand), fraîcheur, volume, dernier passage, liste des fichiers avec leurs positions GTID.
- **Alertes** :
  - « archivage en retard » (avertissement) au-delà de 3 intervalles, et au moins 15 min ;
  - « trou dans l'archive » (critique) si le primaire a purgé un journal avant son archivage. L'alerte reste ouverte jusqu'à la sauvegarde complète suivante.
- **Restauration à un instant précis** (opération `PITR_RESTORE`), sur le serveur de restauration de contrôle :
  1. contrôles préalables : sauvegarde **vérifiée** antérieure à l'instant visé, archive couvrant cet instant, serveur d'exécution disponible ;
  2. restauration de la sauvegarde dans une instance isolée (comme la restauration de contrôle) ;
  3. rejeu des journaux depuis la position GTID de la sauvegarde jusqu'à l'instant visé (exclu). Les transactions déjà présentes dans la sauvegarde ne sont pas rejouées, y compris après une bascule ;
  4. contrôle (bases, tables, nombre de lignes) puis **extraction chiffrée** des tables demandées (export SQL) ;
  5. rapport : dernière transaction rejouée et son heure, nombre de transactions, transactions éventuellement absentes de l'archive.
- `prm-agent decrypt` pour lire une extraction (ou un journal archivé) sur un serveur qui possède la clé.
- Toute demande est tracée dans l'audit avec son motif. Les archivages automatiques réussis n'encombrent ni l'audit ni la liste des opérations : ils restent visibles sur la page Sauvegardes et sont purgés de l'historique après 7 jours.

![Résultat d'une restauration à un instant](images/restauration-instant-resultat.png)

## Vérifié

**Essai au laboratoire** : deux serveurs MariaDB, des ventes simulées toutes les 50 ms et une table de 200 clients.

- Le scénario :
  1. mise à jour de 50 soldes ;
  2. à 21:23:39, suppression accidentelle des clients 101 à 200 ;
  3. nouvelles écritures ensuite.
- Restauration demandée par l'opérateur pour 21:23:39, en partant de la sauvegarde vérifiée de 19:28 (0-2-856) :
  - 776 transactions rejouées depuis 2 journaux en 2 s ;
  - dernière transaction rejouée à 21:23:38.
- La table extraite et déchiffrée contient exactement :
  - les **200 clients**, avec les 50 soldes mis à jour (total 30 650,00, conforme) ;
  - **1 138 ventes**, le même nombre que les ventes de la production antérieures à 21:23:39.
- Une instruction `GRANT` présente dans les journaux a été ignorée et signalée. L'instance isolée n'a pas de comptes, et ces instructions sont sans effet sur les données. Toute autre erreur fait échouer la restauration.

**Tests automatisés** (5 scénarios) :
- planification et intervalle, catalogue ;
- fichiers déjà archivés, alerte de retard ;
- choix de la sauvegarde et des journaux, refus d'un instant futur, avertissement au-delà de l'archive ;
- validation des tables et du motif ;
- suppression des journaux expirés, trou dans l'archive.

Tests de l'agent : contrôles d'activation, tri des erreurs de rejeu.

## Limites

- La restauration est **isolée** : la réinjection en production des lignes retrouvées reste un geste humain (import dans une base de travail, puis copie des lignes). C'est voulu.
- Précision : la seconde. Les transactions validées pendant la seconde visée ne sont pas rejouées.
- Après une **bascule d'urgence avec perte**, les numéros de transaction perdus peuvent être réutilisés par le nouveau primaire. Une restauration qui traverse cette bascule peut alors ignorer quelques transactions du nouveau primaire, et le rapport le signale comme un trou. Après une bascule d'urgence, lancer une sauvegarde complète : les restaurations ultérieures partiront d'elle.
- Le serveur d'archivage doit joindre le primaire sur le port MariaDB. Si le primaire et l'archive sont perdus ensemble (même machine), la perte peut aller jusqu'à un intervalle d'archivage.
- Volume : prévoir sur `backup.root` la taille des binlogs produits entre deux sauvegardes conservées (souvent quelques centaines de Mo par jour pour une pharmacie, à mesurer avec la console : **Volume archivé**).
