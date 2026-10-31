# Périmètre livré — Lot 4 « Haute disponibilité »

Conformément à la réponse 7 du 31/10/2026, la bascule reste **manuelle et assistée** : rien n'est déclenché automatiquement. Le Lot 4 rend la bascule d'urgence sûre et rapide, et supprime la reconfiguration de Prestige après une bascule.

![Ancien primaire isolé et routeur](images/ancien-primaire-isole.png)

## Livré

- **Routeur de connexion** (`prm-agent router`, ou section `router` de l'agent) : adresse fixe pour Prestige. Il dirige chaque nouvelle connexion vers le seul serveur accessible en écriture et qui ne réplique pas, et refuse les connexions s'il y en a zéro ou plusieurs. Il coupe les connexions restées ouvertes vers un ancien primaire. Une sonde manquée isolée est tolérée. Son état est visible sur la fiche du serveur qui l'héberge. Installation sur chaque poste Prestige (sans point unique de panne) ou sur un serveur d'accès. Voir installation §7.
- **Isolement (fencing)** d'un ancien primaire par son agent : lecture seule et coupure des connexions si MariaDB répond ; sinon arrêt du service et passage du démarrage en manuel, pour qu'un redémarrage de la machine ne le remette pas en service. Script `grant-service-control.ps1` pour donner ces droits au compte de l'agent.
- **Bascule d'urgence assistée** (Bascule → onglet Urgence) :
  1. refusée tant que le primaire répond : il faut alors utiliser la bascule planifiée, sans perte ;
  2. choix automatique de la réplique ayant **reçu** le plus de transactions, avec avertissement si l'on en choisit une autre ;
  3. **perte estimée** avant validation, en comparant la dernière position connue du primaire à ce que la réplique a reçu ;
  4. si l'agent de l'ancien primaire répond, isolement par l'agent avant toute promotion. Sinon, la personne doit confirmer que la machine est éteinte ou débranchée. Si l'isolement échoue sans cette confirmation, rien n'est modifié ;
  5. double validation (comme la bascule planifiée), promotion après application de tout ce qui a été reçu, rattachement des autres répliques, inventaire mis à jour ;
  6. l'ancien primaire est marqué **isolé** : incident unique, rattachement refusé, seule la reconstruction depuis une sauvegarde est proposée. Une reconstruction réussie lève l'isolement.
- **Garde-fou permanent** : si un serveur isolé redevient accessible en écriture (redémarrage manuel, par exemple), la console le remet en lecture seule dès le rapport suivant de son agent et ouvre un incident critique. Entre-temps, le routeur refuse les connexions.
- **Procédure sans console** : `prm-agent fence` et `prm-agent promote` sur les serveurs, `prm-agent status` affiche `Gtid_IO_Pos` pour choisir la réplique la plus avancée. Au retour de la console, un administrateur déclare la bascule faite à la main, ce qui aligne l'inventaire et isole l'ancien primaire. Fiche imprimable : [procedure-urgence.md](procedure-urgence.md).
- Rapport d'opération imprimable pour la bascule d'urgence (perte estimée, durée sans primaire, étapes).

## Vérifié

Laboratoire MariaDB à deux serveurs, agents à 30 s d'intervalle de rapport, Prestige simulé par une vente toutes les 50 ms **à travers le routeur**, chaque vente confirmée étant notée :

| Essai | Résultat |
|---|---|
| Arrêt brutal (`kill -9`) du primaire srv-comptoir, bascule d'urgence vers mini-serveur | panne visible dans la console en 15 s ; bascule terminée 21 s après la panne (5,6 s entre l'approbation et la promotion) ; **119 ventes confirmées sur 119** présentes sur le nouveau primaire ; 341 tentatives refusées par le routeur pendant la panne, aucune envoyée au mauvais serveur |
| Même essai dans l'autre sens | bascule terminée 35 s après la panne, isolement puis promotion réussis |
| Ancien primaire relancé à la main puis mis en écriture | routeur : « plusieurs serveurs acceptent les écritures », connexions refusées 18 s ; console : remise en lecture seule automatique 21 s après |
| Reconstruction de l'ancien primaire depuis une sauvegarde vérifiée | isolement levé, réplication repartie (IO/SQL Yes, retard 0) |
| Tests automatisés | 7 scénarios (contrôles, choix de la réplique, perte estimée, échec d'isolement sans effet, bascule complète, remise en lecture seule automatique, déclaration d'une bascule manuelle) ; tests du routeur (routage, refus pendant la bascule, coupure des anciennes connexions, deux primaires, sonde manquée) |

Dans ces essais, les approbations sont scriptées. En réel, le temps de rétablissement est surtout celui des deux personnes : détection (≤ 30 s plus la notification), demande et approbation. L'objectif de moins de 5 minutes reste atteignable si un approbateur est joignable.

## Hors périmètre / limites

- **Pas de bascule automatique**, par choix (réponse 7). Le routeur ne promeut jamais rien : il suit seulement le serveur que la console ou une personne a promu.
- Réplication **asynchrone** : les ventes écrites dans les dernières secondes avant une panne brutale peuvent ne pas avoir été reçues. La perte est estimée, pas évitée. Pour la réduire à zéro, il faudrait la réplication semi-synchrone, qui ralentit chaque vente : à décider avec vous.
- Lors d'un changement de serveur, les transactions en cours sur les connexions coupées sont annulées par MariaDB ; Prestige doit les rejouer (comme après une coupure réseau).
- **Exercices de panne programmés** et **bascule entre sites** (réplique distante ou cloud promue pour une autre pharmacie) : non livrés. La bascule d'urgence fonctionne déjà vers une réplique distante ou cloud de la même topologie. L'orchestration multi-site et les exercices guidés sont proposés pour un lot suivant.
