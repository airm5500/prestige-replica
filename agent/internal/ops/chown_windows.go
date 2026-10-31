//go:build windows

package ops

// Sous Windows, les droits sont hérités du dossier parent (ACL) : rien à faire.
func chownTree(string, string) error { return nil }

func runAsArgs(string) []string { return nil }
