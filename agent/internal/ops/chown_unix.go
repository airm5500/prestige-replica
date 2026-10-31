//go:build !windows

package ops

import (
	"io/fs"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
)

// chownTree attribue récursivement les fichiers à l'utilisateur MariaDB.
// Sans effet si l'agent ne tourne pas en root (fichiers déjà au bon propriétaire).
func chownTree(dir, owner string) error {
	if os.Geteuid() != 0 || owner == "" {
		return nil
	}
	u, err := user.Lookup(owner)
	if err != nil {
		return err
	}
	uid, _ := strconv.Atoi(u.Uid)
	gid, _ := strconv.Atoi(u.Gid)
	return filepath.WalkDir(dir, func(p string, _ fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		return os.Lchown(p, uid, gid)
	})
}

func runAsArgs(owner string) []string {
	if os.Geteuid() == 0 && owner != "" {
		return []string{"--user=" + owner}
	}
	return nil
}
