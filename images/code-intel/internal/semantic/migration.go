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
// bundle until a later release deliberately retires that schema. A hardlink
// marker binds successful application validation to the exact migrated inode.
func prepareDatabaseMigration(databasePath string) (string, error) {
	temporary := databasePath + ".migration"
	rollback := databasePath + ".rollback-v1"
	failed := databasePath + ".failed-v2"
	if err := recoverInterruptedRestore(databasePath, rollback, failed); err != nil {
		return "", err
	}
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
		return pendingMigrationRollback(databasePath, rollback)
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
	if err := clearMigrationValidation(databasePath); err != nil {
		return "", err
	}

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

// pendingMigrationRollback distinguishes a successfully validated migrated
// inode from one activated immediately before a process crash. The marker is a
// hardlink, so an unrelated replacement at the same path cannot inherit trust.
func pendingMigrationRollback(databasePath, rollback string) (string, error) {
	if _, err := os.Stat(rollback); os.IsNotExist(err) {
		return "", nil
	} else if err != nil {
		return "", fail(CodeCorrupt, "The schema v1 rollback database could not be inspected.", false, err)
	}
	databaseInfo, err := os.Stat(databasePath)
	if err != nil {
		return "", fail(CodeCorrupt, "The migrated database could not be inspected for validation.", false, err)
	}
	validated := databasePath + ".validated-v2"
	pending := validated + ".pending"
	if pendingInfo, err := os.Stat(pending); err == nil {
		if !os.SameFile(databaseInfo, pendingInfo) {
			return "", fail(CodeCorrupt, "A pending migration validation marker does not match the active database.", false, nil)
		}
		if err := os.Rename(pending, validated); err != nil {
			return "", fail(CodeCorrupt, "The pending migration validation marker could not be committed.", false, err)
		}
	} else if !os.IsNotExist(err) {
		return "", fail(CodeCorrupt, "The pending migration validation marker could not be inspected.", false, err)
	}
	validatedInfo, err := os.Stat(validated)
	if os.IsNotExist(err) {
		return rollback, nil
	}
	if err != nil {
		return "", fail(CodeCorrupt, "The migration validation marker could not be inspected.", false, err)
	}
	if !os.SameFile(databaseInfo, validatedInfo) {
		return "", fail(CodeCorrupt, "The migration validation marker belongs to a different database inode.", false, nil)
	}
	return "", nil
}

// markMigrationValidated commits trust only after the migrated database has
// passed the application's complete initialization contract.
func markMigrationValidated(databasePath string) error {
	validated := databasePath + ".validated-v2"
	pending := validated + ".pending"
	if _, err := os.Stat(pending); err == nil {
		return fail(CodeCorrupt, "A pending migration validation marker already exists.", false, nil)
	} else if !os.IsNotExist(err) {
		return fail(CodeCorrupt, "The pending migration validation marker could not be inspected.", false, err)
	}
	if _, err := os.Stat(validated); err == nil {
		return fail(CodeCorrupt, "A migration validation marker already exists before commit.", false, nil)
	} else if !os.IsNotExist(err) {
		return fail(CodeCorrupt, "The migration validation marker path could not be inspected.", false, err)
	}
	if err := os.Link(databasePath, pending); err != nil {
		return fail(CodeCorrupt, "The migrated database validation marker could not be prepared.", false, err)
	}
	if err := os.Rename(pending, validated); err != nil {
		if removeErr := os.Remove(pending); removeErr != nil {
			return fail(CodeCorrupt, "The migration validation marker could not be committed or reset.", false, removeErr)
		}
		return fail(CodeCorrupt, "The migration validation marker could not be committed.", false, err)
	}
	return nil
}

// clearMigrationValidation removes derived v2 inode names before migrating an
// active v1 database, including the expected state after an application-level
// rollback replaced v2 with its v1 bundle.
func clearMigrationValidation(databasePath string) error {
	validated := databasePath + ".validated-v2"
	for _, path := range []string{validated + ".pending", validated} {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return fail(CodeCorrupt, "A stale migration validation marker could not be removed.", false, err)
		}
	}
	return nil
}

// recoverInterruptedRestore completes the only state that can remain if the
// process stops after preserving a failed v2 inode but before atomically
// replacing it with rollback-v1. A pending link is committed before the
// rollback replacement; distinct retained files are completed evidence.
func recoverInterruptedRestore(databasePath, rollback, failed string) error {
	databaseInfo, databaseErr := os.Stat(databasePath)
	pending := failed + ".pending"
	if pendingInfo, err := os.Stat(pending); err == nil {
		if databaseErr != nil || !os.SameFile(databaseInfo, pendingInfo) {
			return fail(CodeCorrupt, "An interrupted diagnostic database link does not match the active database.", false, databaseErr)
		}
		// Commit the pending diagnostic name before restoring v1. If the
		// process stops after this rename, the same-inode branch below
		// recognizes the state and completes the restore on the next start.
		if err := os.Rename(pending, failed); err != nil {
			return fail(CodeCorrupt, "An interrupted diagnostic database link could not be committed.", false, err)
		}
		return replaceDatabaseWithRollback(databasePath, rollback)
	} else if !os.IsNotExist(err) {
		return fail(CodeCorrupt, "The pending diagnostic database link could not be inspected.", false, err)
	}
	failedInfo, failedErr := os.Stat(failed)
	if os.IsNotExist(failedErr) {
		return nil
	}
	if failedErr != nil {
		return fail(CodeCorrupt, "The failed migrated database could not be inspected for recovery.", false, failedErr)
	}
	if databaseErr != nil {
		return fail(CodeCorrupt, "The active database could not be inspected for restore recovery.", false, databaseErr)
	}
	if !os.SameFile(databaseInfo, failedInfo) {
		return nil
	}
	return replaceDatabaseWithRollback(databasePath, rollback)
}

// replaceDatabaseWithRollback performs the single atomic namespace change
// that makes the retained v1 inode active again. The rollback path is consumed
// only by a successful replacement, so any failure remains retryable.
func replaceDatabaseWithRollback(databasePath, rollback string) error {
	if _, err := os.Stat(rollback); err != nil {
		return fail(CodeCorrupt, "An interrupted migration restore has no rollback database.", false, err)
	}
	if err := os.Rename(rollback, databasePath); err != nil {
		return fail(CodeCorrupt, "The interrupted migration restore could not be completed atomically.", false, err)
	}
	return nil
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
	pending := failed + ".pending"
	if _, err := os.Stat(pending); err == nil {
		return fail(CodeCorrupt, "A pending failed-migration link already exists and was preserved for diagnosis.", false, nil)
	} else if !os.IsNotExist(err) {
		return fail(CodeCorrupt, "The pending failed-migration link could not be inspected.", false, err)
	}
	if err := os.Link(databasePath, pending); err != nil {
		return fail(CodeCorrupt, "The failed migrated database could not be preserved for diagnosis.", false, err)
	}
	if err := os.Rename(pending, failed); err != nil {
		if removeErr := os.Remove(pending); removeErr != nil {
			return fail(CodeCorrupt, "The failed migrated database link could not be committed or reset.", false, removeErr)
		}
		return fail(CodeCorrupt, "The failed migrated database link could not be committed.", false, err)
	}
	if err := os.Rename(rollbackPath, databasePath); err != nil {
		return fail(CodeCorrupt, "The schema v1 rollback database could not be restored; startup will retry the atomic replacement.", false, err)
	}
	return nil
}
