package recovery

import (
	"encoding/binary"
	"strings"
	"testing"
)

// fakeFRM fabrique un .frm minimal : en-tête, segment extra avec le nom du moteur, forminfo.
func fakeFRM(engine string) []byte {
	b := make([]byte, 4096)
	b[0], b[1], b[2], b[3] = 0xFE, 0x01, 10, 12
	binary.LittleEndian.PutUint16(b[4:6], 0)      // pas d'extra2
	binary.LittleEndian.PutUint32(b[64:68], 3000) // forminfo
	binary.LittleEndian.PutUint16(b[6:8], 1000)   // offset des clés
	binary.LittleEndian.PutUint16(b[14:16], 100)  // longueur des clés
	binary.LittleEndian.PutUint16(b[16:18], 50)   // longueur d'enregistrement
	extra := 1000 + 100 + 50
	binary.LittleEndian.PutUint16(b[extra:], 0) // chaîne de connexion vide
	binary.LittleEndian.PutUint16(b[extra+2:], uint16(len(engine)))
	copy(b[extra+4:], engine)
	binary.LittleEndian.PutUint32(b[55:59], uint32(4+len(engine)))
	binary.LittleEndian.PutUint32(b[10:14], uint32(len(b)))
	copy(b[3000:], "FORMINFO")
	return b
}

func TestPatchFRMEngine(t *testing.T) {
	src := fakeFRM("InnoDB")
	if e, err := FRMEngine(src); err != nil || e != "InnoDB" {
		t.Fatalf("moteur lu %q %v", e, err)
	}
	out, err := PatchFRMEngine(src, "BLACKHOLE", 19)
	if err != nil {
		t.Fatal(err)
	}
	if e, _ := FRMEngine(out); e != "BLACKHOLE" {
		t.Fatalf("moteur après patch %q", e)
	}
	d := len("BLACKHOLE") - len("InnoDB")
	if len(out) != len(src)+d || out[3] != 19 {
		t.Fatalf("taille ou type hérité incorrects")
	}
	if binary.LittleEndian.Uint32(out[10:14]) != uint32(len(out)) {
		t.Fatal("longueur totale non mise à jour")
	}
	fi := binary.LittleEndian.Uint32(out[64:68])
	if string(out[fi:fi+8]) != "FORMINFO" {
		t.Fatal("pointeur forminfo non décalé")
	}
}

func TestDiagnose(t *testing.T) {
	log := `2024-01-01 10:00:00 0 [Note] Starting MariaDB 10.6.12 source revision
2024-01-01 10:00:00 0 [Note] InnoDB: Initializing buffer pool, total size = 128.000MiB
2024-01-01 10:00:01 0 [ERROR] InnoDB: Database page corruption on disk or a failed read of file '.\prestige\vente.ibd' page [page id: space=12, page number=60]
2024-01-01 10:00:01 0 [ERROR] [FATAL] InnoDB: Aborting because of a corrupted database page.
`
	f, clean := Diagnose(log)
	if clean || len(f) == 0 || f[0].Code != "INNODB_CORRUPTION" {
		t.Fatalf("diagnostic inattendu : %+v clean=%v", f, clean)
	}
	f, _ = Diagnose("[Note] Starting MariaDB\n[ERROR] InnoDB: No valid checkpoint was found; the log was created with MariaDB 10.6.12.\n")
	if len(f) == 0 || f[0].Code != "REDO_LOST" {
		t.Fatalf("journal InnoDB non reconnu : %+v", f)
	}
	f, _ = Diagnose("[ERROR] InnoDB: Upgrade after a crash is not supported. The redo log was created with MariaDB 10.4.30.\n")
	if len(f) == 0 || f[0].Code != "VERSION_CRASH" {
		t.Fatalf("version non reconnue : %+v", f)
	}
	_, clean = Diagnose("[Note] InnoDB: Shutdown completed\n[Note] mariadbd: Shutdown complete\n\n")
	if !clean {
		t.Fatal("arrêt propre non reconnu")
	}
}

func TestTextFiles(t *testing.T) {
	trg := keyValues([]byte("TYPE=TRIGGERS\ntriggers='CREATE DEFINER=`root`@`localhost` TRIGGER t1 BEFORE INSERT ON x FOR EACH ROW SET NEW.a=\\'é\\'' 'CREATE TRIGGER t2 AFTER DELETE ON x FOR EACH ROW BEGIN\\n  DELETE FROM y;\\nEND'\nsql_modes=0 0\n"))
	l := quotedList(trg["triggers"])
	if len(l) != 2 || !strings.HasSuffix(l[0], "SET NEW.a='é'") || !strings.Contains(l[1], "BEGIN\n  DELETE") {
		t.Fatalf("déclencheurs mal lus : %q", l)
	}
	if encodeName("stock-2024") != "stock@002d2024" || DecodeFilename("stock@002d2024") != "stock-2024" {
		t.Fatal("encodage des noms de fichiers")
	}
	if safe("../a b") != ".._a_b" && safe("../a b") != "_a_b" {
		t.Fatalf("nom de fichier non assaini : %q", safe("../a b"))
	}
}
