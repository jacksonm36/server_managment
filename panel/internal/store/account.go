package store

import (
	"context"

	"github.com/jackc/pgx/v5"
)

func (s *Store) SetTOTPPending(ctx context.Context, adminID int64, sealed string) error {
	_, err := s.db.Exec(ctx, `UPDATE admins SET totp_pending = $2 WHERE id = $1`, adminID, sealed)
	return err
}

// EnableTOTP promotes the pending secret, records the step used to confirm
// it (so that code cannot be replayed at login) and stores recovery codes.
func (s *Store) EnableTOTP(ctx context.Context, adminID, step int64, recovery [][]byte) error {
	return pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `
			UPDATE admins SET totp_secret = totp_pending, totp_pending = NULL, totp_enabled = true, totp_last_step = $2
			WHERE id = $1 AND totp_pending IS NOT NULL`, adminID, step); err != nil {
			return err
		}
		return replaceRecovery(ctx, tx, adminID, recovery)
	})
}

func (s *Store) DisableTOTP(ctx context.Context, adminID int64) error {
	return pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `
			UPDATE admins SET totp_secret = NULL, totp_pending = NULL, totp_enabled = false, totp_last_step = 0
			WHERE id = $1`, adminID); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `DELETE FROM admin_recovery_codes WHERE admin_id = $1`, adminID)
		return err
	})
}

func replaceRecovery(ctx context.Context, tx pgx.Tx, adminID int64, hashes [][]byte) error {
	if _, err := tx.Exec(ctx, `DELETE FROM admin_recovery_codes WHERE admin_id = $1`, adminID); err != nil {
		return err
	}
	for _, h := range hashes {
		if _, err := tx.Exec(ctx, `INSERT INTO admin_recovery_codes (admin_id, code_hash) VALUES ($1, $2)`, adminID, h); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) ReplaceRecoveryCodes(ctx context.Context, adminID int64, hashes [][]byte) error {
	return pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error { return replaceRecovery(ctx, tx, adminID, hashes) })
}

// UseTOTPStep atomically records a used time step. It returns false if this
// or a later step was already used.
func (s *Store) UseTOTPStep(ctx context.Context, adminID, step int64) (bool, error) {
	tag, err := s.db.Exec(ctx, `UPDATE admins SET totp_last_step = $2 WHERE id = $1 AND totp_last_step < $2`, adminID, step)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

func (s *Store) UseRecoveryCode(ctx context.Context, adminID int64, hash []byte) (bool, error) {
	tag, err := s.db.Exec(ctx, `DELETE FROM admin_recovery_codes WHERE admin_id = $1 AND code_hash = $2`, adminID, hash)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

func (s *Store) RecoveryCodesLeft(ctx context.Context, adminID int64) (int, error) {
	var n int
	err := s.db.QueryRow(ctx, `SELECT count(*) FROM admin_recovery_codes WHERE admin_id = $1`, adminID).Scan(&n)
	return n, err
}

// UpdatePassword sets a new password hash and ends every other session.
func (s *Store) UpdatePassword(ctx context.Context, adminID int64, hash string, keepSession []byte) error {
	return pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE admins SET password_hash = $2, must_change_password = false WHERE id = $1`, adminID, hash); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `DELETE FROM sessions WHERE admin_id = $1 AND token_hash <> $2`, adminID, keepSession)
		return err
	})
}
