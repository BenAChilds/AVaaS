package store

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/benachilds/avaas/internal/model"
)

var ErrInvalidKey = errors.New("invalid API key")

type Principal struct {
	ID     string
	Scopes map[string]bool
}

func GenerateKey(ctx context.Context, s *Store, pepper []byte, description string, scopes []string) (model.APIKey, string, error) {
	description = strings.TrimSpace(description)
	if len(description) > 200 {
		return model.APIKey{}, "", errors.New("description must be at most 200 characters")
	}
	if len(scopes) == 0 {
		scopes = []string{"scan", "admin"}
	}
	idRaw, secretRaw := make([]byte, 9), make([]byte, 32)
	if _, err := rand.Read(idRaw); err != nil {
		return model.APIKey{}, "", err
	}
	if _, err := rand.Read(secretRaw); err != nil {
		return model.APIKey{}, "", err
	}
	id := base64.RawURLEncoding.EncodeToString(idRaw)
	secret := base64.RawURLEncoding.EncodeToString(secretRaw)
	token := "avaas_" + id + "." + secret
	digest := keyDigest(pepper, token)
	created := time.Now().UTC()
	_, err := s.DB.ExecContext(ctx, `INSERT INTO api_keys(id,digest,description,scopes,created_at) VALUES(?,?,?,?,?)`, id, digest, description, scopesText(scopes), timeText(created))
	if err != nil {
		return model.APIKey{}, "", err
	}
	return model.APIKey{ID: id, Description: description, Scopes: scopes, CreatedAt: created}, token, nil
}

func keyDigest(pepper []byte, token string) []byte {
	h := hmac.New(sha256.New, pepper)
	_, _ = h.Write([]byte(token))
	return h.Sum(nil)
}

func Authenticate(ctx context.Context, s *Store, pepper []byte, token string) (Principal, error) {
	if !strings.HasPrefix(token, "avaas_") {
		return Principal{}, ErrInvalidKey
	}
	parts := strings.SplitN(strings.TrimPrefix(token, "avaas_"), ".", 2)
	if len(parts) != 2 || len(parts[0]) < 8 || len(parts[1]) < 32 {
		return Principal{}, ErrInvalidKey
	}
	var digest []byte
	var scopeText string
	var revoked sql.NullString
	err := s.DB.QueryRowContext(ctx, `SELECT digest,scopes,revoked_at FROM api_keys WHERE id=?`, parts[0]).Scan(&digest, &scopeText, &revoked)
	if err != nil || revoked.Valid || subtle.ConstantTimeCompare(digest, keyDigest(pepper, token)) != 1 {
		return Principal{}, ErrInvalidKey
	}
	_, _ = s.DB.ExecContext(ctx, `UPDATE api_keys SET last_used_at=? WHERE id=? AND (last_used_at IS NULL OR last_used_at < ?)`, nowText(), parts[0], timeText(time.Now().UTC().Add(-5*time.Minute)))
	scopes := map[string]bool{}
	for _, v := range parseScopes(scopeText) {
		scopes[v] = true
	}
	return Principal{ID: parts[0], Scopes: scopes}, nil
}

func ListKeys(ctx context.Context, s *Store, includeRevoked bool) ([]model.APIKey, error) {
	q := `SELECT id,description,scopes,created_at,last_used_at,revoked_at,revocation_reason FROM api_keys`
	if !includeRevoked {
		q += ` WHERE revoked_at IS NULL`
	}
	q += ` ORDER BY created_at`
	rows, err := s.DB.QueryContext(ctx, q)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []model.APIKey
	for rows.Next() {
		var k model.APIKey
		var scopes, created string
		var last, revoked sql.NullString
		if err := rows.Scan(&k.ID, &k.Description, &scopes, &created, &last, &revoked, &k.RevocationReason); err != nil {
			return nil, err
		}
		k.Scopes = parseScopes(scopes)
		k.CreatedAt = parseTime(created)
		k.LastUsedAt = nullableTime(last)
		k.RevokedAt = nullableTime(revoked)
		out = append(out, k)
	}
	return out, rows.Err()
}

func RevokeKey(ctx context.Context, s *Store, id, reason string, allowLastAdmin bool) (bool, error) {
	var changed bool
	err := withTx(ctx, s.DB, func(tx *sql.Tx) error {
		var scopes string
		var revoked sql.NullString
		if err := tx.QueryRowContext(ctx, `SELECT scopes,revoked_at FROM api_keys WHERE id=?`, id).Scan(&scopes, &revoked); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrNotFound
			}
			return err
		}
		if revoked.Valid {
			return nil
		}
		if strings.Contains(","+scopes+",", ",admin,") && !allowLastAdmin {
			var n int
			if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM api_keys WHERE revoked_at IS NULL AND (','||scopes||',') LIKE '%,admin,%'`).Scan(&n); err != nil {
				return err
			}
			if n <= 1 {
				return errors.New("refusing to revoke the last active administrative key without --yes")
			}
		}
		res, err := tx.ExecContext(ctx, `UPDATE api_keys SET revoked_at=?,revocation_reason=? WHERE id=? AND revoked_at IS NULL`, nowText(), strings.TrimSpace(reason), id)
		if err != nil {
			return err
		}
		n, _ := res.RowsAffected()
		changed = n > 0
		return nil
	})
	return changed, err
}

func RequireScope(p Principal, scope string) error {
	if p.Scopes[scope] || p.Scopes["admin"] {
		return nil
	}
	return fmt.Errorf("scope %s required", scope)
}
