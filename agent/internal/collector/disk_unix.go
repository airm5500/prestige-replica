//go:build !windows

package collector

import "syscall"

// DiskUsage renvoie l'espace total et disponible pour le chemin.
func DiskUsage(path string) (total, free uint64, err error) {
	var st syscall.Statfs_t
	if err = syscall.Statfs(path, &st); err != nil {
		return 0, 0, err
	}
	bs := uint64(st.Bsize)
	return st.Blocks * bs, st.Bavail * bs, nil
}
