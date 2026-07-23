package extensions

import (
	"path/filepath"
	"strings"
)

// pathWithinRoot compares already-canonical paths. It deliberately accepts
// the root itself and rejects lexical parent traversal on every platform.
func pathWithinRoot(root, path string) bool {
	relative, err := filepath.Rel(root, path)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}
