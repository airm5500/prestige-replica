#!/usr/bin/env bash
# Laboratoire local : un primaire (3307) et une réplique GTID (3308).
# Usage : scripts/lab/start-lab.sh [répertoire]   (défaut /tmp/prm-lab)
# Ne jamais utiliser sur un serveur de production.
set -euo pipefail
LAB=${1:-/tmp/prm-lab}
RUN_USER=${RUN_USER:-$(id -un)}
mkdir -p "$LAB"

start_instance() {
  local name=$1 port=$2 sid=$3 extra=$4
  local dir="$LAB/$name"
  mkdir -p "$dir"
  if [ ! -d "$dir/data/mysql" ]; then
    mariadb-install-db --no-defaults --user="$RUN_USER" --datadir="$dir/data" --auth-root-authentication-method=normal >/dev/null
  fi
  cat > "$dir/my.cnf" <<CNF
[mysqld]
user=$RUN_USER
datadir=$dir/data
socket=$dir/mysqld.sock
pid-file=$dir/mysqld.pid
port=$port
bind-address=127.0.0.1
server_id=$sid
log_bin=$dir/data/binlog
binlog_format=ROW
binlog_row_image=FULL
log_slave_updates=ON
gtid_strict_mode=ON
binlog_expire_logs_seconds=604800
relay_log=$dir/data/relay
log_error=$dir/error.log
$extra
CNF
  mariadbd --defaults-file="$dir/my.cnf" >/dev/null 2>&1 &
  for _ in $(seq 1 30); do
    mariadb --no-defaults -uroot -S "$dir/mysqld.sock" -e 'SELECT 1' >/dev/null 2>&1 && return 0
    sleep 1
  done
  echo "échec du démarrage de $name, voir $dir/error.log" >&2; exit 1
}

sqlp() { mariadb --no-defaults -uroot -S "$LAB/primary/mysqld.sock" "$@"; }
sqlr() { mariadb --no-defaults -uroot -S "$LAB/replica/mysqld.sock" "$@"; }

start_instance primary 3307 1 ""
start_instance replica 3308 2 "read_only=ON"

# Comptes : réplication (privilège minimal) et supervision (lecture seule).
for f in sqlp sqlr; do
  $f -e "SET sql_log_bin=0;
    CREATE USER IF NOT EXISTS 'prm_monitor'@'127.0.0.1' IDENTIFIED BY 'monitor-lab';
    CREATE USER IF NOT EXISTS 'prm_monitor'@'localhost' IDENTIFIED BY 'monitor-lab';
    GRANT BINLOG MONITOR, REPLICA MONITOR, PROCESS, SHOW DATABASES ON *.* TO 'prm_monitor'@'127.0.0.1';
    GRANT BINLOG MONITOR, REPLICA MONITOR, PROCESS, SHOW DATABASES ON *.* TO 'prm_monitor'@'localhost';"
done
sqlp -e "SET sql_log_bin=0;
  CREATE USER IF NOT EXISTS 'prm_repl'@'127.0.0.1' IDENTIFIED BY 'repl-lab';
  GRANT REPLICATION SLAVE ON *.* TO 'prm_repl'@'127.0.0.1';"
# Données de démonstration : écrites AVEC binlog pour être répliquées.
sqlp -e "CREATE DATABASE IF NOT EXISTS prestige_demo;
  CREATE TABLE IF NOT EXISTS prestige_demo.vente (id INT AUTO_INCREMENT PRIMARY KEY, montant DECIMAL(12,2), cree_le TIMESTAMP DEFAULT CURRENT_TIMESTAMP);
  INSERT INTO prestige_demo.vente (montant) VALUES (1500.00),(2750.50);"

# Le laboratoire démarre à vide : la réplique reprend depuis le début du binlog GTID.
if [ -z "$(sqlr -N -e "SHOW ALL SLAVES STATUS" )" ]; then
  sqlr -e "SET GLOBAL gtid_slave_pos='';
    CHANGE MASTER TO MASTER_HOST='127.0.0.1', MASTER_PORT=3307, MASTER_USER='prm_repl', MASTER_PASSWORD='repl-lab', MASTER_USE_GTID=slave_pos;
    START SLAVE;"
fi
sleep 2
sqlr -e "SELECT COUNT(*) AS ventes_repliquees FROM prestige_demo.vente"
echo "Laboratoire prêt : primaire 127.0.0.1:3307, réplique 127.0.0.1:3308 (compte prm_monitor / monitor-lab)"
