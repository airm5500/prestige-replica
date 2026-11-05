# Récupérer les bases d'un serveur mort à partir de ses fichiers

Cas visé : le PC qui faisait tourner MariaDB ne démarre plus (Windows cassé, carte mère, disque système). Le disque est encore lisible : il est branché sur un autre PC (en esclave, en boîtier USB) et on a accès au dossier `data` de MariaDB, voire au dossier MariaDB complet.

`prm-agent recover` essaie **toutes les méthodes connues**, de la plus fidèle à la plus dégradée, table par table, et produit un script SQL prêt à restaurer et un rapport en français. **La source n'est jamais modifiée** : tout se fait sur une copie.

Pas besoin de console, ni de configuration, ni que l'agent ait été installé sur la machine morte.

## En pratique (Windows)

Sur le PC où le disque est branché, PowerShell **en administrateur**, dans le dossier du kit :

```powershell
.\4-recuperer-disque.ps1 -Source "E:\Program Files\MariaDB 10.6\data"
```

Le script range tout dans `C:\Recuperation\<date>` (`-Destination` pour un autre disque : prévoir **deux fois la taille du dossier data**), puis ouvre le rapport. Équivalent direct :

```powershell
prm-agent.exe recover -source "E:\Program Files\MariaDB 10.6\data" -work C:\Recuperation\travail -out C:\Recuperation\resultat
```

Ensuite, sur le serveur MariaDB de remplacement :

```powershell
mariadb -u root -p < C:\Recuperation\resultat\restauration-complete.sql
```

### Quels binaires MariaDB ?

Après un arrêt brutal, **seule la même version** de MariaDB sait appliquer le journal InnoDB de l'ancien serveur. L'outil prend, dans l'ordre :
1. le dossier donné par `-bin` ;
2. le dossier `bin` à côté du dossier `data` copié (`…\MariaDB 10.6\bin` si tout le dossier MariaDB a été copié : c'est le meilleur cas) ;
3. le MariaDB installé sur le PC.

La version qui a écrit les fichiers est lue dans `ib_logfile0` ; un écart de version est signalé dans le rapport.

## Les méthodes, dans l'ordre

| Étape | Méthode | Quand elle sert |
|---|---|---|
| 1 | **Inventaire** (lecture seule) : bases, tables, moteur de chaque table lu dans son `.frm`, fichiers système présents ou absents, version, binlogs | toujours |
| 2 | **Diagnostic de l'ancien journal d'erreurs** (`.err`) : arrêt propre ou brutal, pages endommagées, journal InnoDB illisible, version différente, droits, Aria, mémoire, disque plein | toujours ; chaque constat est expliqué dans le rapport |
| 3 | **Copie de travail** du dossier data | toujours ; la copie prend les droits du PC actuel (contourne les droits NTFS de l'ancien Windows) |
| 4 | **Serveur temporaire sur la copie**, sans comptes (`skip-grant-tables` : le mot de passe root n'est pas nécessaire), sur 127.0.0.1 et un port libre, avec les réglages InnoDB de l'ancien `my.ini` ; **export table par table** | arrêt propre ou coupure de courant : le journal InnoDB est appliqué normalement |
| 5 | Même chose avec **`innodb_force_recovery` de 1 à 6** ; seules les tables pas encore exportées sont retentées à chaque niveau | journal ou pages endommagés, serveur qui refuse de démarrer |
| 6 | **Sauvetage ligne à ligne** : si une table s'interrompt à la lecture (page illisible), ses lignes sont copiées par tranches dans l'ordre de la clé, depuis le début puis depuis la fin, et les lignes du milieu relues une à une par leur clé | pages endommagées dans une table : seules les lignes des pages abîmées sont perdues |
| 7 | **Import du fichier `.ibd`** dans un serveur neuf : structure lue dans le `.frm`, données rattachées (`DISCARD`/`IMPORT TABLESPACE`), en essayant les formats de ligne | `ibdata1` ou `ib_logfile0` absents ou détruits, ou serveur impossible à démarrer même au niveau 6 |
| 8 | **Tables Aria/MyISAM** recopiées puis réparées (`REPAIR TABLE`, puis `EXTENDED`, puis `USE_FRM`) ; **journaux Aria** illisibles mis de côté (dans la copie) | tables système (comptes, procédures) ou tables non InnoDB endommagées |
| 9 | **Vues, déclencheurs, procédures, comptes** : exportés par le serveur temporaire, ou relus directement dans leurs fichiers (`.frm` de vue, `.TRG`, tables système copiées) si aucun serveur n'a pu démarrer | toujours |
| 10 | **Binlogs** trouvés sur le disque : copiés, résumés (période couverte, dernier GTID), et exportés en SQL à partir d'une date (`-binlogs-depuis`) | pour reprendre les dernières transactions si le journal InnoDB est perdu, ou après une restauration de sauvegarde |

Une table récupérée à une étape n'est plus retentée ensuite. Si le serveur temporaire s'arrête net en lisant une table abîmée, il est relancé au même niveau et passe à la table suivante.

## Résultat

Dans le dossier `-out` :
- **`RAPPORT.txt`** : verdict, diagnostic, essais, pour chaque table la méthode qui a marché, son nombre de lignes et ses avertissements, et les étapes suivantes ; `rapport.json` pour la machine ;
- **`restauration-complete.sql`** : toutes les tables récupérées, puis les vues, puis les procédures et déclencheurs ;
- `sql/<base>/<table>.sql` : un fichier autonome par table (pour ne restaurer qu'une table) ;
- `comptes.sql` : comptes et droits de l'ancien serveur, **à part** : à appliquer seulement sur un serveur neuf de même version ;
- `binlogs/` et, avec `-binlogs-depuis`, `binlogs-depuis.sql` ;
- `journaux/` : journaux d'erreurs des serveurs temporaires.

Le code de sortie vaut 0 si tout est récupéré, 3 s'il reste des tables en échec, 1 en cas d'erreur.

## Options

| Option | Rôle |
|---|---|
| `-source` | dossier `data` récupéré (obligatoire, jamais modifié) |
| `-work` | dossier de travail vide, hors de la source |
| `-out` | dossier de résultat |
| `-bin` | dossier `bin` d'un MariaDB de même version que l'ancien serveur |
| `-my-ini` | ancien `my.ini` (sinon cherché dans `data` et à côté) |
| `-max-force` | niveau maximal d'`innodb_force_recovery` (6 par défaut) |
| `-bases` | ne récupérer que ces bases (`prestige,stock`) |
| `-diagnostic` | inventaire, diagnostic et contrôle des pages (`innochecksum`) seulement, sans rien démarrer ni copier |
| `-binlogs-depuis` | exporter en SQL les binlogs trouvés à partir de cette date (`"2026-11-05 08:00:00"`) |
| `-buffer-mo` | mémoire InnoDB du serveur temporaire (512 Mo par défaut) |

## Vérifié au laboratoire (MariaDB 10.11)

Deux bases (dont une au nom spécial `stock-2024`), sept tables : InnoDB avec clé primaire, sans clé primaire (index unique), avec clé étrangère, avec BLOB ; une table Aria, une MyISAM ; une vue, un déclencheur, une procédure. Après chaque récupération, le script a été restauré sur un autre serveur et les tables comparées (`CHECKSUM TABLE`, empreintes ligne à ligne).

| Panne simulée | Méthode retenue par l'outil | Résultat |
|---|---|---|
| Arrêt propre | démarrage normal | 7/7 tables, vue, déclencheur, procédure ; **empreintes identiques** |
| Coupure brutale (`kill -9`) pendant 80 000 insertions | démarrage normal (journal InnoDB appliqué) | 7/7 tables, toutes les transactions validées présentes ; binlog résumé (400 transactions, dernier GTID) |
| `ibdata1` et `ib_logfile0` supprimés | import des `.ibd` (structure tirée des `.frm`), réparation Aria/MyISAM, objets relus dans les fichiers | 7/7 tables, **empreintes identiques**, vue, déclencheur, procédure et comptes récupérés |
| 3 pages détruites au milieu d'une table de 30 000 lignes | sauvetage ligne à ligne | 29 968 lignes sauvées, **toutes identiques à l'original** ; 34 perdues (celles des 3 pages) ; les 6 autres tables intactes |
| Points de reprise du journal InnoDB détruits | `innodb_force_recovery=6` après échec des niveaux 0 à 5 | 7/7 tables, avertissement « dernières transactions peut-être absentes » et renvoi vers les binlogs trouvés, qui couvrent la période |
| Fichier de contrôle Aria détruit | journaux Aria mis de côté, nouvel essai | 7/7 tables |

Tests automatisés : lecture et modification des `.frm`, diagnostic des journaux d'erreurs, lecture des fichiers de déclencheurs et de vues.

## Limites

- **Clés étrangères** : avec l'import des `.ibd` (étape 7), elles ne sont pas recréées (elles ne sont pas dans le `.frm`). Le rapport le signale pour chaque table.
- **Table sans `.frm`** (fichier `.ibd` seul) : la structure est inconnue. La reprendre d'une ancienne sauvegarde ou de la réplique, puis importer à la main.
- **Tables partitionnées** et tables dont les données sont dans `ibdata1` (sans `innodb_file_per_table`) : récupérées seulement si le serveur temporaire démarre (étapes 4 à 6).
- **Disque physiquement défaillant** : copier d'abord son contenu avec un outil de copie de disque (en ignorant les secteurs illisibles), puis lancer la récupération sur cette copie.
- Les transactions qui n'étaient que dans un journal InnoDB détruit ne sont pas dans les fichiers de tables : elles ne se retrouvent que dans les binlogs ou la réplique.
- Toujours **comparer avec la réplique et la dernière sauvegarde** de l'agent avant de remettre en service : une réplique à jour reste la meilleure source quand elle existe (bascule d'urgence, [procédure d'urgence](procedure-urgence.md)).
