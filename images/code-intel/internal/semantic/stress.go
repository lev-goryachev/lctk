package semantic

import (
	"context"
	"fmt"
	"strconv"
	"time"
)

// PopulateStressCorpus writes deterministic rows through the production schema
// for release-scale measurement. Publication metadata is written only after all
// rows commit, so an interrupted run remains visibly not ready.
func (s *Store) PopulateStressCorpus(ctx context.Context, count int) error {
	if count < 1 || count > 1_000_000 {
		return fmt.Errorf("stress corpus count must be between 1 and 1000000")
	}
	var existing int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM semantic_chunks").Scan(&existing); err != nil {
		return err
	}
	if existing != 0 {
		return fmt.Errorf("stress corpus requires an empty semantic store")
	}
	vector := make([]float32, s.config.Dimensions)
	vector[0] = 1
	encoded, err := serializeVector(vector, s.config.Dimensions)
	if err != nil {
		return err
	}
	const batchSize = 10_000
	for first := 0; first < count; first += batchSize {
		if err := ctx.Err(); err != nil {
			return err
		}
		last := min(first+batchSize, count)
		transaction, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		fileStatement, err := transaction.PrepareContext(ctx,
			"INSERT INTO semantic_files(path, digest) VALUES(?, ?)")
		if err != nil {
			transaction.Rollback()
			return err
		}
		chunkStatement, err := transaction.PrepareContext(ctx, `INSERT INTO semantic_chunks(
id, stable_id, path, content_digest, language, precision, anchor, ordinal,
start_line, end_line, content, embedding_text) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
		if err != nil {
			fileStatement.Close()
			transaction.Rollback()
			return err
		}
		vectorStatement, err := transaction.PrepareContext(ctx,
			"INSERT INTO semantic_vectors(rowid, embedding) VALUES(?, ?)")
		if err != nil {
			fileStatement.Close()
			chunkStatement.Close()
			transaction.Rollback()
			return err
		}
		for index := first; index < last; index++ {
			id := index + 1
			path := fmt.Sprintf("src/%03d/file-%07d.go", index/10_000, index)
			stableID := "stress-" + strconv.Itoa(index)
			if _, err := fileStatement.ExecContext(ctx, path, stableID); err != nil {
				fileStatement.Close()
				chunkStatement.Close()
				vectorStatement.Close()
				transaction.Rollback()
				return err
			}
			if _, err := chunkStatement.ExecContext(ctx, id, stableID, path, stableID,
				"Go", "syntax", "StressSymbol"+strconv.Itoa(index), 0, 1, 1,
				"func StressSymbol() {}", "search_document: synthetic stress symbol"); err != nil {
				fileStatement.Close()
				chunkStatement.Close()
				vectorStatement.Close()
				transaction.Rollback()
				return err
			}
			if _, err := vectorStatement.ExecContext(ctx, id, encoded); err != nil {
				fileStatement.Close()
				chunkStatement.Close()
				vectorStatement.Close()
				transaction.Rollback()
				return err
			}
		}
		fileStatement.Close()
		chunkStatement.Close()
		vectorStatement.Close()
		if err := transaction.Commit(); err != nil {
			return err
		}
	}
	transaction, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	for key, value := range map[string]string{
		"generation": "1",
		"indexed_at": time.Now().UTC().Format(time.RFC3339),
	} {
		if _, err := transaction.ExecContext(ctx,
			"INSERT INTO semantic_meta(key, value) VALUES(?, ?) ON CONFLICT(key) DO UPDATE SET value=excluded.value",
			key, value); err != nil {
			transaction.Rollback()
			return err
		}
	}
	return transaction.Commit()
}
