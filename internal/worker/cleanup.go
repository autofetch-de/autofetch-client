package worker

import (
	"encoding/json"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const DefaultPartialTTL = 7 * 24 * time.Hour

type partMeta struct {
	URL string `json:"url"`
}

// CleanupPartialsTTL keeps recent partials (for resume) and removes old/invalid ones.
// Rules:
// - Keep *.part if it is younger than ttl AND has a valid *.part.meta.json.
// - Remove *.part if meta is missing/invalid OR it is older than ttl.
// - Remove orphan *.part.meta.json if corresponding *.part is missing.
// This avoids accumulating trash while still allowing resume for recent jobs.
func CleanupPartialsTTL(baseDir string, ttl time.Duration) error {
	baseDir = strings.TrimSpace(baseDir)
	if baseDir == "" {
		return nil
	}
	if ttl <= 0 {
		ttl = DefaultPartialTTL
	}

	// If baseDir doesn't exist yet, nothing to clean.
	if _, err := os.Stat(baseDir); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}

	now := time.Now()
	var removedPart, removedMeta, orphanMeta int

	err := filepath.WalkDir(baseDir, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			log.Printf("cleanup: skip %s: %v", path, walkErr)
			return nil
		}
		if d.IsDir() {
			return nil
		}

		nameLower := strings.ToLower(d.Name())

		// Orphan meta cleanup
		if strings.HasSuffix(nameLower, ".part.meta.json") {
			partPath := strings.TrimSuffix(path, ".meta.json") // remove trailing ".meta.json"
			if _, err := os.Stat(partPath); err != nil && os.IsNotExist(err) {
				_ = os.Remove(path)
				orphanMeta++
			}
			return nil
		}

		// Part files cleanup
		if strings.HasSuffix(nameLower, ".part") {
			info, err := d.Info()
			if err != nil {
				log.Printf("cleanup: info fail %s: %v", path, err)
				return nil
			}

			age := now.Sub(info.ModTime())
			metaPath := path + ".meta.json"

			// Must have a valid meta to keep (prevents resuming wrong file)
			metaOK := false
			if b, err := os.ReadFile(metaPath); err == nil {
				var m partMeta
				if json.Unmarshal(b, &m) == nil && strings.TrimSpace(m.URL) != "" {
					metaOK = true
				}
			}

			// Keep recent + metaOK, else delete (and delete meta too if exists)
			if age <= ttl && metaOK {
				return nil
			}

			_ = os.Remove(path)
			removedPart++

			if err := os.Remove(metaPath); err == nil {
				removedMeta++
			}
			return nil
		}

		return nil
	})

	if removedPart > 0 || removedMeta > 0 || orphanMeta > 0 {
		log.Printf("cleanup: removed part=%d meta=%d orphan_meta=%d under %s", removedPart, removedMeta, orphanMeta, baseDir)
	}
	return err
}
