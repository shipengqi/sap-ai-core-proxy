package agentconfig

import (
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Apply writes all planned changes. Every existing file is copied to a
// timestamped backup first, then replaced by a temp file + rename so a
// crash cannot leave a half-written config.
func Apply(changes []FileChange, backup bool) ([]string, error) {
	var backups []string
	for _, change := range changes {
		if !change.Changed() {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(change.Path), 0755); err != nil {
			return backups, fmt.Errorf("create directory for %s: %w", change.Path, err)
		}
		if len(change.Old) > 0 && backup {
			path := fmt.Sprintf("%s.bak.%s", change.Path, time.Now().Format("20060102-150405"))
			if err := os.WriteFile(path, change.Old, 0600); err != nil {
				return backups, fmt.Errorf("backup %s: %w", change.Path, err)
			}
			backups = append(backups, path)
		}
		if err := atomicWrite(change.Path, change.New, change.Mode); err != nil {
			return backups, err
		}
	}
	return backups, nil
}

func atomicWrite(path string, data []byte, mode os.FileMode) error {
	if mode == 0 {
		mode = 0644
	}
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".aicoreproxy-*")
	if err != nil {
		return fmt.Errorf("create temp file for %s: %w", path, err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("write %s: %w", path, err)
	}
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return fmt.Errorf("chmod %s: %w", path, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close %s: %w", path, err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		// Some Windows filesystems refuse to rename over an existing file.
		// A backup already exists, so fall back to a direct write.
		if werr := os.WriteFile(path, data, mode); werr != nil {
			return fmt.Errorf("replace %s: %w", path, err)
		}
	}
	return nil
}
