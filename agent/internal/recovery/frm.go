// Package recovery récupère les données d'un serveur MariaDB à partir de ses fichiers bruts
// (dossier data copié depuis un disque) : diagnostic, démarrage isolé sur une copie avec
// innodb_force_recovery croissant, import table par table des fichiers .ibd, export SQL.
// Les fichiers d'origine ne sont jamais modifiés.
package recovery

import (
	"encoding/binary"
	"errors"
	"fmt"
)

// Disposition d'un fichier .frm (MariaDB 10.x) :
//
//	0      en-tête de 64 octets (0xFE 0x01, version, type de moteur historique…)
//	64     segment extra2 (longueur en [4:6]), puis position du bloc forminfo (4 octets)
//	[6:8]  début des index ; leur longueur en [14:16] (ou [47:51] si 0xFFFF)
//	       puis l'enregistrement des valeurs par défaut (longueur en [16:18])
//	       puis le segment « extra » (longueur en [55:59]) : chaîne de connexion, NOM DU MOTEUR…
//	       puis forminfo, colonnes, noms…
//	[10:14] longueur totale du fichier
//
// Pour relire la structure d'une table InnoDB sans son dictionnaire, on recopie son .frm en
// changeant le moteur pour BLACKHOLE (qui n'a besoin d'aucun fichier de données) : le serveur
// accepte alors SHOW CREATE TABLE, qui restitue la définition exacte (colonnes, défauts, index,
// jeux de caractères, ROW_FORMAT, commentaires).

const frmHeader = 64

type frmLayout struct {
	extra2Len  int
	formInfoAt int // position du pointeur vers forminfo
	enginePos  int // position de la longueur du nom de moteur
	engineLen  int
}

func parseFRM(b []byte) (frmLayout, error) {
	var l frmLayout
	if len(b) < frmHeader+8 || b[0] != 0xFE || b[1] != 0x01 {
		return l, errors.New("fichier .frm invalide (signature)")
	}
	le := binary.LittleEndian
	l.extra2Len = int(le.Uint16(b[4:6]))
	l.formInfoAt = frmHeader + l.extra2Len
	keyOff := int(le.Uint16(b[6:8]))
	keyLen := int(le.Uint16(b[14:16]))
	if keyLen == 0xFFFF {
		keyLen = int(le.Uint32(b[47:51]))
	}
	recLen := int(le.Uint16(b[16:18]))
	extra := keyOff + keyLen + recLen
	if l.formInfoAt+4 > len(b) || extra+4 > len(b) {
		return l, errors.New("fichier .frm tronqué")
	}
	conn := int(le.Uint16(b[extra : extra+2]))
	l.enginePos = extra + 2 + conn
	if l.enginePos+2 > len(b) {
		return l, errors.New("fichier .frm tronqué (moteur)")
	}
	l.engineLen = int(le.Uint16(b[l.enginePos : l.enginePos+2]))
	if l.enginePos+2+l.engineLen > len(b) || l.engineLen > 64 {
		return l, errors.New("fichier .frm : nom de moteur illisible")
	}
	return l, nil
}

// legacyEngines : moteurs des très anciens .frm sans nom de moteur.
var legacyEngines = map[byte]string{6: "MEMORY", 9: "MyISAM", 12: "InnoDB", 17: "CSV", 19: "BLACKHOLE", 27: "Aria"}

// FRMEngine renvoie le moteur de stockage déclaré dans un .frm.
func FRMEngine(b []byte) (string, error) {
	l, err := parseFRM(b)
	if err != nil {
		return "", err
	}
	if l.engineLen > 0 {
		return string(b[l.enginePos+2 : l.enginePos+2+l.engineLen]), nil
	}
	if e, ok := legacyEngines[b[3]]; ok {
		return e, nil
	}
	return "", fmt.Errorf("moteur inconnu (type %d)", b[3])
}

// PatchFRMEngine renvoie une copie du .frm déclarant un autre moteur (BLACKHOLE, MEMORY…).
func PatchFRMEngine(b []byte, engine string, legacy byte) ([]byte, error) {
	l, err := parseFRM(b)
	if err != nil {
		return nil, err
	}
	le := binary.LittleEndian
	d := len(engine) - l.engineLen
	out := make([]byte, 0, len(b)+d)
	out = append(out, b[:l.enginePos]...)
	out = le.AppendUint16(out, uint16(len(engine)))
	out = append(out, engine...)
	out = append(out, b[l.enginePos+2+l.engineLen:]...)
	out[3] = legacy
	le.PutUint32(out[10:14], uint32(len(out)))
	le.PutUint32(out[55:59], uint32(int(le.Uint32(out[55:59]))+d))
	le.PutUint32(out[l.formInfoAt:l.formInfoAt+4], uint32(int(le.Uint32(out[l.formInfoAt:l.formInfoAt+4]))+d))
	return out, nil
}
