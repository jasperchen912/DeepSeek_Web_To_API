package chathistory

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

func (s *sqliteStore) Start(params StartParams) (Entry, error) {
	if s == nil {
		return Entry{}, errors.New("chat history sqlite store is nil")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return Entry{}, s.err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return Entry{}, fmt.Errorf("begin chat history sqlite start: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	limit, err := s.metaIntLocked(tx, "limit", DefaultLimit)
	if err != nil {
		return Entry{}, err
	}
	if limit == DisabledLimit {
		return Entry{}, ErrDisabled
	}
	now := time.Now().UnixMilli()
	revision, err := s.nextRevisionLocked(tx)
	if err != nil {
		return Entry{}, err
	}
	status := strings.TrimSpace(params.Status)
	if status == "" {
		status = "streaming"
	}
	entry := Entry{
		ID:          "chat_" + strings.ReplaceAll(uuid.NewString(), "-", ""),
		Revision:    revision,
		CreatedAt:   now,
		UpdatedAt:   now,
		Status:      status,
		CallerID:    strings.TrimSpace(params.CallerID),
		AccountID:   strings.TrimSpace(params.AccountID),
		Model:       strings.TrimSpace(params.Model),
		Stream:      params.Stream,
		UserInput:   strings.TrimSpace(params.UserInput),
		Messages:    cloneMessages(params.Messages),
		HistoryText: params.HistoryText,
		FinalPrompt: strings.TrimSpace(params.FinalPrompt),
	}
	if err := s.upsertEntryLocked(tx, entry); err != nil {
		return Entry{}, err
	}
	if err := s.pruneLocked(tx); err != nil {
		return Entry{}, err
	}
	if err := tx.Commit(); err != nil {
		return Entry{}, fmt.Errorf("commit chat history sqlite start: %w", err)
	}
	return cloneEntry(entry), nil
}

func (s *sqliteStore) Update(id string, params UpdateParams) (Entry, error) {
	if s == nil {
		return Entry{}, errors.New("chat history sqlite store is nil")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return Entry{}, s.err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return Entry{}, fmt.Errorf("begin chat history sqlite update: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	item, err := s.getEntryTxLocked(tx, strings.TrimSpace(id))
	if err != nil {
		return Entry{}, err
	}
	now := time.Now().UnixMilli()
	revision, err := s.nextRevisionLocked(tx)
	if err != nil {
		return Entry{}, err
	}
	item.Revision = revision
	item.UpdatedAt = now
	if params.CallerID != "" {
		item.CallerID = strings.TrimSpace(params.CallerID)
	}
	if params.AccountID != "" {
		item.AccountID = strings.TrimSpace(params.AccountID)
	}
	if params.Status != "" {
		item.Status = params.Status
	}
	if params.HistoryText != "" {
		item.HistoryText = params.HistoryText
	}
	if params.FinalPrompt != "" {
		item.FinalPrompt = strings.TrimSpace(params.FinalPrompt)
	}
	item.ReasoningContent = params.ReasoningContent
	item.Content = params.Content
	item.Error = strings.TrimSpace(params.Error)
	item.StatusCode = params.StatusCode
	item.ElapsedMs = params.ElapsedMs
	item.FinishReason = strings.TrimSpace(params.FinishReason)
	if params.Usage != nil {
		item.Usage = cloneMap(params.Usage)
	}
	if params.Completed {
		item.CompletedAt = now
	}
	if err := s.upsertEntryLocked(tx, item); err != nil {
		return Entry{}, err
	}
	if err := s.pruneLocked(tx); err != nil {
		return Entry{}, err
	}
	if err := tx.Commit(); err != nil {
		return Entry{}, fmt.Errorf("commit chat history sqlite update: %w", err)
	}
	return cloneEntry(item), nil
}

func (s *sqliteStore) Delete(id string) error {
	return s.withWriteTx("delete", func(tx *sql.Tx) error {
		id = strings.TrimSpace(id)
		if id == "" {
			return errors.New("history id is required")
		}
		result, err := tx.Exec(`DELETE FROM chat_history WHERE id = ?`, id)
		if err != nil {
			return fmt.Errorf("delete chat history sqlite row: %w", err)
		}
		affected, _ := result.RowsAffected()
		if affected == 0 {
			return errors.New("chat history entry not found")
		}
		_, err = s.nextRevisionLocked(tx)
		return err
	})
}

func (s *sqliteStore) Clear() error {
	return s.withWriteTx("clear", func(tx *sql.Tx) error {
		if _, err := tx.Exec(`DELETE FROM chat_history`); err != nil {
			return fmt.Errorf("clear chat history sqlite rows: %w", err)
		}
		_, err := s.nextRevisionLocked(tx)
		return err
	})
}

func (s *sqliteStore) SetLimit(limit int) (File, error) {
	if s == nil {
		return File{}, errors.New("chat history sqlite store is nil")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return File{}, s.err
	}
	if !isAllowedLimit(limit) {
		return File{}, fmt.Errorf("unsupported chat history limit: %d", limit)
	}
	tx, err := s.db.Begin()
	if err != nil {
		return File{}, fmt.Errorf("begin chat history sqlite set limit: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := s.setMetaLocked(tx, "limit", fmt.Sprintf("%d", limit)); err != nil {
		return File{}, err
	}
	if _, err := s.nextRevisionLocked(tx); err != nil {
		return File{}, err
	}
	if err := s.pruneLocked(tx); err != nil {
		return File{}, err
	}
	if err := tx.Commit(); err != nil {
		return File{}, fmt.Errorf("commit chat history sqlite set limit: %w", err)
	}
	return s.snapshotLocked()
}

func (s *sqliteStore) withWriteTx(name string, fn func(*sql.Tx) error) error {
	if s == nil {
		return errors.New("chat history sqlite store is nil")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return s.err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("begin chat history sqlite %s: %w", name, err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := fn(tx); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit chat history sqlite %s: %w", name, err)
	}
	return nil
}

func (s *sqliteStore) getEntryTxLocked(tx *sql.Tx, id string) (Entry, error) {
	if id == "" {
		return Entry{}, errors.New("history id is required")
	}
	var detailJSON, detailEncoding string
	var detailBlob []byte
	err := tx.QueryRow(`SELECT detail_json, detail_encoding, detail_blob FROM chat_history WHERE id = ?`, id).Scan(&detailJSON, &detailEncoding, &detailBlob)
	if errors.Is(err, sql.ErrNoRows) {
		return Entry{}, errors.New("chat history entry not found")
	}
	if err != nil {
		return Entry{}, fmt.Errorf("read chat history sqlite detail: %w", err)
	}
	return decodeSQLiteDetail(detailJSON, detailEncoding, detailBlob)
}

func (s *sqliteStore) upsertEntryLocked(tx *sql.Tx, item Entry) error {
	summary := summaryFromEntry(item)
	if strings.TrimSpace(summary.ID) == "" {
		return errors.New("chat history id is required")
	}
	usageJSON, err := encodeUsageJSON(summary.Usage)
	if err != nil {
		return err
	}
	detailJSON, detailEncoding, detailBlob, err := encodeSQLiteDetail(item)
	if err != nil {
		return err
	}
	_, err = tx.Exec(
		`INSERT INTO chat_history (
			id, revision, created_at, updated_at, completed_at, status, caller_id,
			account_id, model, stream, user_input, preview, status_code, elapsed_ms,
			finish_reason, detail_revision, usage_json, detail_json, detail_encoding, detail_blob
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			revision = excluded.revision,
			created_at = excluded.created_at,
			updated_at = excluded.updated_at,
			completed_at = excluded.completed_at,
			status = excluded.status,
			caller_id = excluded.caller_id,
			account_id = excluded.account_id,
			model = excluded.model,
			stream = excluded.stream,
			user_input = excluded.user_input,
			preview = excluded.preview,
			status_code = excluded.status_code,
			elapsed_ms = excluded.elapsed_ms,
			finish_reason = excluded.finish_reason,
			detail_revision = excluded.detail_revision,
			usage_json = excluded.usage_json,
			detail_json = excluded.detail_json,
			detail_encoding = excluded.detail_encoding,
			detail_blob = excluded.detail_blob`,
		summary.ID,
		summary.Revision,
		summary.CreatedAt,
		summary.UpdatedAt,
		summary.CompletedAt,
		summary.Status,
		summary.CallerID,
		summary.AccountID,
		summary.Model,
		boolToSQLiteInt(summary.Stream),
		summary.UserInput,
		summary.Preview,
		summary.StatusCode,
		summary.ElapsedMs,
		summary.FinishReason,
		summary.DetailRevision,
		usageJSON,
		detailJSON,
		detailEncoding,
		detailBlob,
	)
	if err != nil {
		return fmt.Errorf("write chat history sqlite row: %w", err)
	}
	return nil
}
