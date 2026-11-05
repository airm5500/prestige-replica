package recovery

import (
	"os"
	"regexp"
	"strings"
)

// Finding : constat tiré d'un journal d'erreurs MariaDB, avec la méthode qu'il appelle.
type Finding struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Advice  string `json:"advice"`
	Line    string `json:"line,omitempty"`
}

type rule struct {
	code, message, advice string
	re                    *regexp.Regexp
}

// Règles appliquées au dernier démarrage consigné dans le journal ; l'ordre fixe la priorité.
var rules = []rule{
	{"VERSION_CRASH", "Version de MariaDB différente de celle qui a écrit les fichiers, après un arrêt brutal",
		"Utiliser exactement la même version (dossier MariaDB récupéré sur le disque, option -bin) : une version plus récente ne sait pas reprendre un journal InnoDB d'une autre version après une panne.",
		regexp.MustCompile(`(?i)upgrade after a crash is not supported|unsupported redo log format|redo log was created with|log sequence number .* is in the future|Upgrade after a crash`)},
	{"REDO_LOST", "Journal InnoDB (ib_logfile0) illisible ou incohérent",
		"Démarrage sans le journal (innodb_force_recovery=6) : les tables sont lues telles qu'enregistrées sur le disque ; les toutes dernières transactions, restées dans le journal, sont à reprendre dans les binlogs ou la dernière sauvegarde.",
		regexp.MustCompile(`(?i)No valid checkpoint|log sequence number .* (is|was) (in the future|ahead)|Missing FILE_CHECKPOINT|corrupted redo log|Log file .* corrupt|Malformed log record|ib_logfile0 .*(not found|missing)`)},
	{"PERMISSIONS", "Accès refusé aux fichiers (droits NTFS de l'ancien Windows)",
		"La copie de travail faite par l'outil est créée avec les droits du poste actuel. Si la lecture de la source échoue, lancer l'outil en administrateur, ou reprendre la propriété de la copie de sécurité (takeown /F <dossier> /R).",
		regexp.MustCompile(`(?i)permission denied|errno:? ?13\b|OS error: ?5\b|Access is denied|Operating system error number 5`)},
	{"INNODB_CORRUPTION", "Pages InnoDB endommagées",
		"Démarrage avec innodb_force_recovery croissant (1 à 6) sur une copie, puis export table par table ; les tables encore illisibles sont réimportées depuis leur fichier .ibd.",
		regexp.MustCompile(`(?i)database page corruption|page .* is corrupted|checksum mismatch|Corrupted page|Assertion failure|Failing assertion|Index corruption|Apparent corruption|Trying to read .* page number .* which is outside|page.* (was|is) marked as corrupted`)},
	{"MISSING_TABLESPACE", "Fichier de table InnoDB absent ou illisible",
		"Les tables concernées seront signalées ; les autres restent récupérables. Vérifier que la copie du dossier data est complète.",
		regexp.MustCompile(`(?i)tablespace .* (was not found|is missing)|Cannot open datafile|Could not find a valid tablespace file|is in the data dictionary but its tablespace is missing`)},
	{"REDO_MISMATCH", "Taille ou format du journal InnoDB inattendu",
		"Les réglages InnoDB de l'ancien my.ini (innodb_log_file_size, innodb_page_size, innodb_data_file_path) sont repris automatiquement ; sinon, fournir l'ancien my.ini (option -my-ini).",
		regexp.MustCompile(`(?i)innodb_log_file_size|log file .* is of different size|innodb_page_size|data file .* is of a different size|innodb_data_file_path`)},
	{"ARIA", "Journaux ou tables Aria endommagés (tables système, comptes)",
		"Réparation des tables système Aria avec aria_chk sur la copie ; les journaux aria_log sont mis de côté si nécessaire. Les données InnoDB n'en dépendent pas.",
		regexp.MustCompile(`(?i)aria recovery failed|aria engine: .*corrupt|aria_log|Got error .* from storage engine Aria|Table '.*' is marked as crashed|mysql\.(proc|user|db|global_priv|plugin|servers).* (crashed|corrupt)`)},
	{"PRIVILEGES", "Tables des comptes illisibles",
		"L'instance de récupération démarre sans contrôle des comptes (skip-grant-tables) : le mot de passe root n'est pas nécessaire.",
		regexp.MustCompile(`(?i)Can't open and lock privilege tables|Fatal error: Can't open and lock privilege tables`)},
	{"MEMORY", "Mémoire insuffisante pour l'ancien réglage",
		"L'instance de récupération utilise un petit cache InnoDB (256 Mo), indépendant de l'ancien réglage.",
		regexp.MustCompile(`(?i)Cannot allocate memory for the buffer pool|out of memory|mmap\(.*\) failed`)},
	{"PORT", "Port déjà utilisé",
		"L'instance de récupération choisit un port libre automatiquement.",
		regexp.MustCompile(`(?i)Do you already have another (mysqld|mariadbd) server running|Address already in use|bind on TCP/IP port`)},
	{"DISK_FULL", "Disque plein",
		"Libérer de la place : la copie de travail et l'export demandent environ deux fois la taille du dossier data.",
		regexp.MustCompile(`(?i)no space left on device|disk is full|Errcode: 28|OS error: ?112\b`)},
}

var startRe = regexp.MustCompile(`(?i)(starting MariaDB|mysqld \(server|mariadbd \(server|InnoDB: Initializing buffer pool|Starting crash recovery|mariadbd: ready for connections|mysqld: ready for connections)`)

// LastStart renvoie la partie du journal depuis le dernier démarrage (au plus 400 lignes).
func LastStart(log string) string {
	lines := strings.Split(strings.ReplaceAll(log, "\r\n", "\n"), "\n")
	begin := 0
	for i := len(lines) - 1; i >= 0; i-- {
		if strings.Contains(lines[i], "InnoDB: Initializing buffer pool") || strings.Contains(strings.ToLower(lines[i]), "starting mariadb") ||
			strings.Contains(lines[i], "(server ") || strings.Contains(lines[i], "InnoDB: Uses event mutexes") || strings.Contains(lines[i], "InnoDB: Compressed tables use") {
			begin = i
			// Remonter jusqu'au début du bloc de démarrage.
			for begin > 0 && begin > i-15 && !strings.Contains(lines[begin-1], "Shutdown complete") && !strings.Contains(lines[begin-1], "ready for connections") {
				begin--
			}
			break
		}
	}
	if len(lines)-begin > 400 {
		begin = len(lines) - 400
	}
	return strings.Join(lines[begin:], "\n")
}

// Diagnose analyse un journal d'erreurs ; le second résultat indique un dernier arrêt propre.
func Diagnose(log string) ([]Finding, bool) {
	part := LastStart(log)
	var out []Finding
	seen := map[string]bool{}
	for _, l := range strings.Split(part, "\n") {
		for _, r := range rules {
			if !seen[r.code] && r.re.MatchString(l) {
				seen[r.code] = true
				out = append(out, Finding{Code: r.code, Message: r.message, Advice: r.advice, Line: strings.TrimSpace(trim(l, 300))})
			}
		}
	}
	trimmed := strings.TrimSpace(log)
	clean := strings.HasSuffix(lastNonEmpty(trimmed), "Shutdown complete") || strings.Contains(lastLines(trimmed, 3), "Shutdown complete")
	return out, clean
}

// DiagnoseFile lit et analyse un fichier .err.
func DiagnoseFile(path string) ([]Finding, bool, string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, false, "", err
	}
	f, clean := Diagnose(string(b))
	return f, clean, lastLines(string(b), 30), nil
}

func trim(s string, n int) string {
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}

func lastNonEmpty(s string) string {
	lines := strings.Split(strings.ReplaceAll(s, "\r\n", "\n"), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if t := strings.TrimSpace(lines[i]); t != "" {
			return t
		}
	}
	return ""
}

func lastLines(s string, n int) string {
	lines := strings.Split(strings.ReplaceAll(strings.TrimRight(s, "\r\n "), "\r\n", "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}
