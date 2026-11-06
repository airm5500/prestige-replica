# Procédure d'urgence : primaire perdu

À imprimer et à garder près du serveur. Elle sert dans deux cas : la console fonctionne (cas A, recommandé), ou la console est injoignable (cas B, à la main).

Règle d'or : **un seul serveur doit accepter les ventes**. Si l'ancien primaire repart en écriture alors qu'une réplique a été promue, les ventes se répartissent sur deux bases qui ne se rejoindront plus. On isole donc toujours l'ancien primaire **avant** de promouvoir.

## 0. Est-ce vraiment une panne du primaire ?

- Prestige affiche des erreurs de connexion sur **tous** les postes, pas sur un seul.
- La console (page Parc) montre le primaire en rouge, « MariaDB injoignable » ou « aucun rapport ».
- Un simple redémarrage de Windows ou du service MariaDB prend moins de 5 minutes : si c'est le cas, attendez plutôt que basculer. Une bascule d'urgence oblige ensuite à reconstruire l'ancien primaire (environ 30 à 60 minutes pour 15 Go).

## Cas A : la console répond

1. Console → **Bascule** → onglet **Urgence (primaire perdu)** → choisir la pharmacie → **Lancer les contrôles préalables**.
2. Lire la **perte estimée** : nombre de transactions du primaire que la réplique n'a pas reçues avant la panne.
3. Si la machine de l'ancien primaire est injoignable (éteinte, plus de réseau), la console demande de confirmer qu'elle est **éteinte ou débranchée du réseau**. Le faire réellement avant de cocher.
4. Saisir le nom de la topologie et demander l'approbation. Une **seconde personne** (rôle Approbateur) approuve en ressaisissant le code de la pharmacie.
5. La console isole l'ancien primaire (lecture seule, ou arrêt du service MariaDB et démarrage automatique désactivé), puis promeut la réplique après application de tout ce qu'elle a reçu, puis rattache les autres répliques.
6. Si les postes passent par le **routeur de connexion**, Prestige repart tout seul. Sinon, changer l'adresse du serveur dans Prestige (adresse rappelée dans le rapport).
7. Plus tard, sur la fiche de l'ancien primaire : **Reconstruire depuis une sauvegarde vérifiée**. Ne jamais le rattacher tel quel.

## Cas B : la console est injoignable

Prérequis : l'agent est installé avec `replicationControl.enabled: true` sur chaque serveur (voir installation §6). Commandes dans une invite **administrateur** :

```
cd "C:\Program Files\PrestigeReplicaManager"
set CFG=C:\ProgramData\PrestigeReplicaManager\agent.json
```

### B1. Isoler l'ancien primaire

- Si la machine répond encore (MariaDB planté mais Windows démarré) :
  ```
  prm-agent fence -config %CFG%
  ```
  Si MariaDB tourne, il passe en lecture seule et les connexions sont coupées. S'il ne répond pas, arrêtez le service et désactivez son démarrage automatique :
  ```
  net stop MariaDB
  sc config MariaDB start= demand
  ```
- Si la machine ne répond plus : **débrancher le câble réseau ou l'éteindre**, et coller une étiquette « NE PAS RALLUMER — base isolée le … à … ».

### B2. Choisir la réplique la plus avancée

Sur chaque réplique :

```
prm-agent status -config %CFG%
```

Comparer `Gtid_IO_Pos` (dernière transaction reçue) : le dernier nombre le plus grand l'emporte (`0-1-48211` est plus avancé que `0-1-48190`). Ignorer une réplique différée.

### B3. Promouvoir

Sur la réplique choisie :

```
prm-agent promote -config %CFG%
```

L'agent attend que tout ce qui a été reçu soit appliqué (5 minutes au plus), arrête la réplication puis ouvre le serveur en écriture. La sortie JSON indique `"promoted": true` et la position finale.

### B4. Reconnecter Prestige

- Avec le routeur de connexion : rien à faire, il détecte le nouveau primaire en une à deux secondes.
- Sans routeur : changer l'adresse du serveur MariaDB dans la configuration de Prestige sur chaque poste.

### B5. Les autres répliques

S'il en reste, elles se rattacheront depuis la console à son retour (fiche du serveur → **Rattacher au primaire**). À la main, sur chacune, avec le compte d'administration :

```sql
STOP SLAVE;
CHANGE MASTER TO MASTER_HOST='<nouveau primaire>', MASTER_PORT=3306,
  MASTER_USER='prm_repl', MASTER_PASSWORD='<mot de passe>', MASTER_USE_GTID=slave_pos;
START SLAVE;
```

### B6. Au retour de la console

Un administrateur déclare la bascule : **Bascule → Urgence → « Bascule déjà faite à la main »**, choisir le serveur promu. L'inventaire est mis à jour et l'ancien primaire est marqué isolé. Ensuite, le reconstruire depuis une sauvegarde (cas A, étape 7).

## Après l'incident

- Imprimer le **rapport d'opération** (Opérations → l'opération → « Rapport d'opération imprimable »).
- Si des ventes ont été perdues (perte estimée non nulle), les ressaisir à partir des tickets imprimés pendant la période indiquée.
- Remettre le démarrage automatique de MariaDB sur l'ancien primaire **après** sa reconstruction : `sc config MariaDB start= auto`.
- Un ancien primaire isolé par l'agent reste en lecture seule même s'il est relancé : c'est voulu. Ne lever l'isolement (`prm-agent unfence`) qu'après une reconstruction faite à la main.

## S'entraîner

Une fois par trimestre au moins : Bascule → **Exercice**, programmé à une heure de fermeture. La console rappelle l'échéance. Lire ensuite le rapport avec l'équipe et vérifier que cette fiche est toujours à jour (noms des serveurs, adresses, personnes à appeler).
