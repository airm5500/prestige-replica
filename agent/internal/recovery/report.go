package recovery

import (
	"fmt"
	"strings"
)

func (j *job) verdict(fatal error) {
	r := j.r
	total := len(j.order)
	switch {
	case fatal != nil:
		r.Verdict = "Récupération interrompue : " + fatal.Error()
	case j.o.DiagnoseOnly:
		r.Verdict = fmt.Sprintf("Diagnostic seulement : %d base(s), %d table(s) trouvées.", len(j.inv.Databases), total)
	case total == 0:
		r.Verdict = "Aucune table utilisateur trouvée dans la source."
	case r.Failed == 0:
		r.Verdict = fmt.Sprintf("Toutes les tables ont été récupérées (%d).", total)
	case r.Recovered == 0:
		r.Verdict = fmt.Sprintf("Aucune table récupérée sur %d.", total)
	default:
		r.Verdict = fmt.Sprintf("%d table(s) récupérée(s) sur %d ; %d en échec.", r.Recovered, total, r.Failed)
	}
	r.Next = nil
	add := func(s string) { r.Next = append(r.Next, s) }
	if r.VersionWarning != "" {
		add(r.VersionWarning)
	}
	degraded := false
	for _, t := range r.Tables {
		if t.Status == "RECUPEREE" && t.Warning != "" {
			degraded = true
		}
	}
	if r.Recovered > 0 {
		add("Restaurer sur un serveur MariaDB sain (même version de préférence) : mariadb -u root -p < restauration-complete.sql")
		add("Vérifier les données métier (derniers tickets, stocks, caisses) avant de rouvrir les postes.")
	}
	if degraded {
		add("Certaines tables ont été récupérées en mode dégradé (voir les avertissements) : compléter avec la dernière sauvegarde de l'agent et ses binlogs archivés (console, page Sauvegardes, restauration à un instant donné) si des lignes manquent.")
	}
	forced := false
	for _, t := range r.Tables {
		if t.Status == "RECUPEREE" && strings.Contains(t.Method, "innodb_force_recovery=") && !strings.HasSuffix(t.Method, "=1") && !strings.HasSuffix(t.Method, "=2") && !strings.HasSuffix(t.Method, "=3") {
			forced = true
		}
	}
	if forced && len(r.Binlogs) > 0 {
		first, last := r.Binlogs[0].First, r.Binlogs[len(r.Binlogs)-1].Last
		add(fmt.Sprintf("Le journal InnoDB n'a pas pu être appliqué : les dernières transactions peuvent manquer. Les binlogs trouvés couvrent %s → %s : les exporter en SQL (-binlogs-depuis) et comparer avec les données récupérées avant de rejouer ce qui manque (attention aux doublons).", firstNon(first, "?"), firstNon(last, "?")))
	}
	if r.Failed > 0 {
		add("Pour les tables en échec : partir de la dernière sauvegarde vérifiée de l'agent, puis rejouer les binlogs archivés (console) ou ceux trouvés sur le disque (-binlogs-depuis).")
		if !strings.EqualFold(r.ToolsVersion, j.inv.Version) && j.inv.Version != "" {
			add(fmt.Sprintf("Relancer avec les binaires MariaDB %s (option -bin) : la même version lit mieux ces fichiers.", j.inv.Version))
		}
		if j.o.MaxForce < 6 {
			add(fmt.Sprintf("Relancer avec -max-force 6 (niveau maximal utilisé : %d).", j.o.MaxForce))
		}
	}
	if len(r.Binlogs) > 0 && r.BinlogSQL == "" && !forced {
		add("Des binlogs ont été trouvés et copiés dans binlogs/ : ils permettent de rejouer les dernières transactions après une restauration de sauvegarde (option -binlogs-depuis \"AAAA-MM-JJ HH:MM:SS\").")
	}
	if contains(j.inv.Missing, "mysql (comptes)") {
		add("Le dossier mysql (comptes) est absent : recréer les comptes à la main sur le nouveau serveur.")
	}
}

// TextReport : rapport lisible, en français.
func TextReport(r *Report, diagnoseOnly bool) string {
	var b strings.Builder
	w := func(f string, a ...any) { fmt.Fprintf(&b, f+"\n", a...) }
	w("RAPPORT DE RÉCUPÉRATION MariaDB — Prestige Replica Manager")
	w("Début : %s   Fin : %s", r.StartedAt.Format("02/01/2006 15:04:05"), r.FinishedAt.Format("02/01/2006 15:04:05"))
	w("")
	w(">>> %s", r.Verdict)
	w("")
	if inv := r.Inventory; inv != nil {
		w("SOURCE : %s", inv.Source)
		w("  %d fichiers, %.1f Mo ; écrits par MariaDB %s (%s)", inv.FileCount, float64(inv.TotalBytes)/1e6, firstNon(inv.Version, "version inconnue"), inv.VersionSource)
		w("  fichiers système : %s ; journal InnoDB : %s ; journaux Aria : %s", list(inv.SystemFiles), list(inv.RedoFiles), list(inv.AriaLogs))
		if len(inv.Missing) > 0 {
			w("  ABSENTS : %s", strings.Join(inv.Missing, ", "))
		}
		for _, d := range inv.Databases {
			w("  base %s : %d table(s), %d vue(s)", d.Name, len(d.Tables), len(d.Views))
		}
	}
	w("BINAIRES : MariaDB %s (%s)", firstNon(r.ToolsVersion, "?"), r.ServerBinary)
	if r.VersionWarning != "" {
		w("  ATTENTION : %s", r.VersionWarning)
	}
	w("")
	w("DIAGNOSTIC DE L'ANCIEN JOURNAL D'ERREURS")
	if r.OldLogTail == "" {
		w("  aucun fichier .err trouvé")
	} else if r.CleanShutdown {
		w("  dernier arrêt : propre")
	} else {
		w("  dernier arrêt : brutal (panne, coupure, plantage)")
	}
	for _, f := range r.Findings {
		w("  - %s : %s", f.Message, f.Advice)
		if f.Line != "" {
			w("      « %s »", f.Line)
		}
	}
	if len(r.Damaged) > 0 {
		w("  pages invalides (innochecksum) dans : %s", strings.Join(r.Damaged, ", "))
	}
	if len(r.Repairs) > 0 {
		w("")
		w("CORRECTIONS APPLIQUÉES (sur la copie de travail)")
		for _, x := range r.Repairs {
			w("  - %s", x)
		}
	}
	if len(r.Attempts) > 0 {
		w("")
		w("ESSAIS")
		for _, a := range r.Attempts {
			if a.Started {
				w("  ✓ %s : démarré", a.Method)
			} else {
				w("  ✗ %s : %s", a.Method, firstNon(oneLine(a.Log), a.Error))
			}
		}
	}
	if !diagnoseOnly && len(r.Tables) > 0 {
		w("")
		w("TABLES")
		for _, t := range r.Tables {
			if t.Status == "RECUPEREE" {
				w("  ✓ %s.%s : %d lignes — %s — %s", t.Database, t.Table, t.Rows, t.Method, t.File)
				if t.Warning != "" {
					w("      avertissement : %s", t.Warning)
				}
			} else {
				w("  ✗ %s.%s : %s", t.Database, t.Table, short(t.Error))
			}
		}
	}
	if len(r.Extras) > 0 {
		w("")
		w("AUTRES OBJETS")
		for _, x := range r.Extras {
			w("  - %s", x)
		}
	}
	if len(r.Binlogs) > 0 {
		w("")
		w("JOURNAUX BINAIRES TROUVÉS (copiés dans binlogs/)")
		for _, bl := range r.Binlogs {
			if bl.Error != "" {
				w("  %s : %s", bl.Name, bl.Error)
				continue
			}
			w("  %s : %d transactions, du %s au %s, dernier GTID %s", bl.Name, bl.Events, firstNon(bl.First, "?"), firstNon(bl.Last, "?"), firstNon(bl.LastGTID, "?"))
		}
		if r.BinlogSQL != "" {
			w("  export SQL : %s", r.BinlogSQL)
		}
	}
	if r.FullRestore != "" {
		w("")
		w("RÉSULTAT : %s (tables, puis vues, puis procédures et déclencheurs)", r.FullRestore)
	}
	if len(r.Next) > 0 {
		w("")
		w("ÉTAPES SUIVANTES")
		for i, x := range r.Next {
			w("  %d. %s", i+1, x)
		}
	}
	return b.String()
}

func list(s []string) string {
	if len(s) == 0 {
		return "aucun"
	}
	return strings.Join(s, ", ")
}

func oneLine(s string) string {
	return short(strings.ReplaceAll(s, "\n", " | "))
}
