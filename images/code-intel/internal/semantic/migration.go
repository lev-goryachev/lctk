package semantic

import (
	"database/sql"
	"io"
	"os"
	"path/filepath"

	_ "github.com/ncruces/go-sqlite3/driver"
)

// prepareDatabaseMigration upgrades schema v1 on a durable copy, validates the
// copy, and swaps it atomically. The original remains beside it as the rollback
// bundle until a later release deliberately retires that schema.
func prepareDatabaseMigration(databasePath string) (string, error) {
	if _, err := os.Stat(databasePath); os.IsNotExist(err) {
		return "", nil
	} else if err != nil {
		return "", fail(CodeCorrupt, "The project intelligence database could not be inspected.", false, err)
	}
	current, err := openMigrationDatabase(databasePath)
	if err != nil {
		return "", err
	}
	var version int
	if err := current.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		current.Close()
		return "", fail(CodeCorrupt, "The project intelligence schema version could not be read.", false, err)
	}
	if version != 1 {
		if err := current.Close(); err != nil {
			return "", fail(CodeCorrupt, "The project intelligence database could not be closed after preflight.", false, err)
		}
		return "", nil
	}
	if _, err := current.Exec("PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
		current.Close()
		return "", fail(CodeCorrupt, "The legacy database could not be checkpointed before migration.", false, err)
	}
	if err := current.Close(); err != nil {
		return "", fail(CodeCorrupt, "The legacy database could not be closed before migration.", false, err)
	}
	if err := removeEmptySidecars(databasePath); err != nil {
		return "", err
	}

	temporary := databasePath + ".migration"
	rollback := databasePath + ".rollback-v1"
	if err := resetInterruptedMigration(databasePath, temporary, rollback); err != nil {
		return "", err
	}
	if err := copyDatabaseFile(databasePath, temporary); err != nil {
		return "", err
	}
	defer os.Remove(temporary)
	migrated, err := openMigrationDatabase(temporary)
	if err != nil {
		return "", err
	}
	if _, err := migrated.Exec("BEGIN IMMEDIATE; " + schemaDDL() + " COMMIT;"); err != nil {
		migrated.Close()
		return "", fail(CodeCorrupt, "The copied project intelligence schema could not be migrated.", false, err)
	}
	var integrity string
	if err := migrated.QueryRow("PRAGMA integrity_check").Scan(&integrity); err != nil || integrity != "ok" {
		migrated.Close()
		return "", fail(CodeCorrupt, "The migrated project intelligence copy failed its integrity check.", false, err)
	}
	if _, err := migrated.Exec("PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
		migrated.Close()
		return "", fail(CodeCorrupt, "The migrated project intelligence copy could not be checkpointed.", false, err)
	}
	if err := migrated.Close(); err != nil {
		return "", fail(CodeCorrupt, "The migrated project intelligence copy could not be closed.", false, err)
	}
	if err := removeEmptySidecars(temporary); err != nil {
		return "", err
	}
	// The state volume is a Linux filesystem. A hardlink preserves the exact v1
	// inode without copying its bytes, while the active database name remains
	// continuously valid until the single atomic replacement below commits v2.
	if err := os.Link(databasePath, rollback); err != nil {
		return "", fail(CodeInternalError, "The original project intelligence database could not become the rollback bundle.", false, err)
	}
	if err := os.Rename(temporary, databasePath); err != nil {
		if removeErr := os.Remove(rollback); removeErr != nil {
			return "", fail(CodeCorrupt, "Migration activation failed and its uncommitted rollback link could not be removed.", false, removeErr)
		}
		return "", fail(CodeInternalError, "The migrated project intelligence database could not be activated.", false, err)
	}
	return rollback, nil
}

// resetInterruptedMigration recognizes only the safe pre-commit crash states.
// Before activation, semantic.db and rollback-v1 are hardlinks to the same v1
// inode; any migration copy is derived and can be discarded. Distinct files are
// an actual retained rollback bundle and are never guessed away.
func resetInterruptedMigration(databasePath, temporary, rollback string) error {
	databaseInfo, err := os.Stat(databasePath)
	if err != nil {
		return fail(CodeCorrupt, "The project intelligence database could not be inspected for migration recovery.", false, err)
	}
	rollbackInfo, rollbackErr := os.Stat(rollback)
	switch {
	case rollbackErr == nil:
		if !os.SameFile(databaseInfo, rollbackInfo) {
			return fail(CodeCorrupt, "A distinct schema v1 rollback bundle already exists; retire or restore it before another migration.", false, nil)
		}
		if err := os.Remove(rollback); err != nil {
			return fail(CodeCorrupt, "An interrupted migration rollback link could not be reset.", false, err)
		}
	case !os.IsNotExist(rollbackErr):
		return fail(CodeCorrupt, "The migration rollback bundle could not be inspected.", false, rollbackErr)
	}
	if err := os.Remove(temporary); err != nil && !os.IsNotExist(err) {
		return fail(CodeCorrupt, "An interrupted migration copy could not be reset.", false, err)
	}
	return nil
}

func removeEmptySidecars(databasePath string) error {
	for _, suffix := range []string{"-wal", "-shm"} {
		path := databasePath + suffix
		info, err := os.Stat(path)
		switch {
		case os.IsNotExist(err):
			continue
		case err != nil:
			return fail(CodeCorrupt, "A SQLite migration sidecar could not be inspected.", false, err)
		case info.Size() != 0:
			return fail(CodeCorrupt, "A non-empty SQLite sidecar remained after the migration checkpoint.", false, nil)
		default:
			if err := os.Remove(path); err != nil {
				return fail(CodeCorrupt, "An empty SQLite migration sidecar could not be removed.", false, err)
			}
		}
	}
	return nil
}

func openMigrationDatabase(path string) (*sql.DB, error) {
	db, err := sql.Open("sqlite3", "file:"+filepath.ToSlash(path)+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=synchronous(FULL)")
	if err != nil {
		return nil, fail(CodeCorrupt, "The project intelligence database could not be opened for migration.", false, err)
	}
	db.SetMaxOpenConns(1)
	return db, nil
}

func copyDatabaseFile(source, destination string) error {
	input, err := os.Open(source)
	if err != nil {
		return fail(CodeCorrupt, "The legacy database could not be opened for copying.", false, err)
	}
	defer input.Close()
	output, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return fail(CodeInternalError, "The migration copy could not be created.", false, err)
	}
	if _, err := io.Copy(output, input); err != nil {
		output.Close()
		return fail(CodeInternalError, "The legacy database could not be copied.", false, err)
	}
	if err := output.Sync(); err != nil {
		output.Close()
		return fail(CodeInternalError, "The migration copy could not be flushed.", false, err)
	}
	if err := output.Close(); err != nil {
		return fail(CodeInternalError, "The migration copy could not be closed.", false, err)
	}
	return nil
}

func restoreMigratedDatabase(databasePath, rollbackPath string) error {
	if rollbackPath == "" {
		return nil
	}
	failed := databasePath + ".failed-v2"
	if _, err := os.Stat(failed); err == nil {
		return fail(CodeCorrupt, "A failed migrated database already exists and was preserved for diagnosis.", false, nil)
	} else if !os.IsNotExist(err) {
		return fail(CodeCorrupt, "The failed migrated database path could not be inspected.", false, err)
	}
	if err := os.Link(databasePath, failed); err != nil {
		return fail(CodeCorrupt, "The failed migrated database could not be preserved for diagnosis.", false, err)
	}
	if err := os.Rename(rollbackPath, databasePath); err != nil {
		if removeErr := os.Remove(failed); removeErr != nil {
			return fail(CodeCorrupt, "Migration rollback failed and its uncommitted diagnostic link could not be removed.", false, removeErr)
		}
		return fail(CodeCorrupt, "The schema v1 rollback database could not be restored.", false, err)
	}
	return nil
}
