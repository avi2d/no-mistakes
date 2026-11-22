package db

import "fmt"

func (d *DB) AddRunScratchFile(runID, path string) error {
	_, err := d.sql.Exec("INSERT OR IGNORE INTO run_scratch_files (run_id, path) VALUES (?, ?)", runID, path)
	if err != nil {
		return fmt.Errorf("record scratch file: %w", err)
	}
	return nil
}

func (d *DB) RunScratchFiles(runID string) ([]string, error) {
	rows, err := d.sql.Query("SELECT path FROM run_scratch_files WHERE run_id = ? ORDER BY path", runID)
	if err != nil {
		return nil, fmt.Errorf("read scratch files: %w", err)
	}
	defer rows.Close()
	var paths []string
	for rows.Next() {
		var path string
		if err := rows.Scan(&path); err != nil {
			return nil, fmt.Errorf("read scratch file: %w", err)
		}
		paths = append(paths, path)
	}
	return paths, rows.Err()
}
