package db

import (
	"database/sql"
	"errors"
	"time"

	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

const connectBusyWindow = 5 * time.Second

func OpenSQLite(dsn string) (*sql.DB, error) {
	database, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	database.SetMaxOpenConns(1)
	// Switching a new file to WAL returns SQLITE_BUSY without consulting
	// busy_timeout, so a concurrent first open waits here instead.
	deadline := time.Now().Add(connectBusyWindow)
	for {
		err = database.Ping()
		if err == nil {
			return database, nil
		}
		if !isBusy(err) || time.Now().After(deadline) {
			_ = database.Close()
			return nil, err
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func isBusy(err error) bool {
	var sqliteErr *sqlite.Error
	return errors.As(err, &sqliteErr) && sqliteErr.Code()&0xff == sqlite3.SQLITE_BUSY
}
