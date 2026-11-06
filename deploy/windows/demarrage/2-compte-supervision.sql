-- Étape 2 : à exécuter UNE fois sur le serveur MariaDB de la pharmacie (HeidiSQL, ou « mysql -u root -p »).
-- Compte en lecture seule : il ne voit aucune donnée, seulement l'état de la réplication et du serveur.
-- Remplacez le mot de passe par le vôtre et notez-le : il servira à l'étape 3.
CREATE USER 'prm_monitor'@'localhost' IDENTIFIED BY 'Changez-moi-mot-de-passe-long';
GRANT BINLOG MONITOR, REPLICA MONITOR, PROCESS, SHOW DATABASES ON *.* TO 'prm_monitor'@'localhost';
