package main

import (
	"flag"
	"fmt"
	"os"
	"runtime"
	"strings"

	"github.com/airm5500/prestige-replica-manager/agent/internal/recovery"
)

// recoverCmd : récupération des bases à partir des fichiers bruts d'un serveur MariaDB
// (disque d'une machine morte). Ne demande ni configuration ni console.
func recoverCmd(args []string) int {
	fs := flag.NewFlagSet("recover", flag.ExitOnError)
	source := fs.String("source", "", "dossier data récupéré (jamais modifié)")
	work := fs.String("work", "", "dossier de travail vide (copie des fichiers), sur un disque avec 2 fois la place")
	out := fs.String("out", "", "dossier de résultat (exports SQL, rapport)")
	bin := fs.String("bin", "", "dossier bin de MariaDB de MÊME version que l'ancien serveur (recommandé)")
	myIni := fs.String("my-ini", "", "ancien my.ini (sinon cherché à côté du dossier data)")
	maxForce := fs.Int("max-force", 6, "niveau maximal d'innodb_force_recovery (0 à 6)")
	bases := fs.String("bases", "", "bases à récupérer, séparées par des virgules (défaut : toutes)")
	diag := fs.Bool("diagnostic", false, "inventaire et diagnostic seulement, sans rien démarrer")
	since := fs.String("binlogs-depuis", "", "exporter en SQL les binlogs trouvés à partir de cette date (\"AAAA-MM-JJ HH:MM:SS\")")
	runAs := fs.String("run-as", "", "Linux : utilisateur du serveur temporaire (défaut : root si lancé en root)")
	buffer := fs.Int("buffer-mo", 512, "mémoire InnoDB du serveur temporaire, en Mo")
	fs.Usage = func() {
		fmt.Fprint(os.Stderr, `prm-agent recover : récupère les bases d'un serveur MariaDB à partir de ses fichiers
(disque d'une machine morte branché sur un autre poste). La source n'est jamais modifiée.

Exemple (Windows, en administrateur) :
  prm-agent recover -source "E:\Program Files\MariaDB 10.6\data" -work D:\recup\travail -out D:\recup\resultat

Méthodes, dans l'ordre, jusqu'à ce que chaque table soit récupérée :
  1. démarrage d'un serveur temporaire sur une COPIE, puis export table par table ;
  2. même chose avec innodb_force_recovery de 1 à 6 ;
  3. tables restantes : structure lue dans le .frm, données rattachées depuis le .ibd ;
     tables Aria/MyISAM : recopiées puis réparées.
Les binlogs trouvés sont copiés et résumés. Résultat : RAPPORT.txt, restauration-complete.sql.

Options :
`)
		fs.PrintDefaults()
	}
	_ = fs.Parse(args)
	if *source == "" || *out == "" || (*work == "" && !*diag) {
		fs.Usage()
		return 2
	}
	o := recovery.Options{Source: *source, Work: *work, Out: *out, BinDir: *bin, MyIni: *myIni, MaxForce: *maxForce,
		DiagnoseOnly: *diag, BinlogsSince: *since, RunAs: *runAs, BufferMB: *buffer,
		Log: func(s string) { fmt.Println(s) }}
	if *bases != "" {
		for _, b := range strings.Split(*bases, ",") {
			if b = strings.TrimSpace(b); b != "" {
				o.Databases = append(o.Databases, b)
			}
		}
	}
	if runtime.GOOS != "windows" && o.RunAs == "" && os.Geteuid() == 0 {
		o.RunAs = "root"
	}
	ctx, cancel := signalContext()
	defer cancel()
	r, err := recovery.Run(ctx, o)
	if r != nil {
		fmt.Println()
		fmt.Print(recovery.TextReport(r, *diag))
		fmt.Printf("\nRapport : %s\n", *out)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "ÉCHEC :", err)
		return 1
	}
	if r.Failed > 0 {
		return 3
	}
	return 0
}
