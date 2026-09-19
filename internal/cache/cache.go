// Package cache stores verdicts by finding id, so unchanged code is never
// scored twice.
package cache

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"

	bolt "go.etcd.io/bbolt"

	"github.com/SergeAx/scrutus/internal/core"
)

var bucket = []byte("verdicts")

type Store struct {
	db   *bolt.DB
	salt string
	ttl  time.Duration
}

type entry struct {
	Verdict core.Verdict `json:"verdict"`
	Stored  time.Time    `json:"stored"`
}

// Open prepares the store. A corrupt file is recreated rather than failing the
// run; the caller decides whether to warn.
func Open(dir, apiKey string, ttl time.Duration) (*Store, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, "verdicts.db")
	db, err := bolt.Open(path, 0o600, &bolt.Options{Timeout: 5 * time.Second})
	if err != nil {
		if removeErr := os.Remove(path); removeErr != nil {
			return nil, err
		}
		db, err = bolt.Open(path, 0o600, &bolt.Options{Timeout: 5 * time.Second})
		if err != nil {
			return nil, err
		}
	}
	if err := db.Update(func(tx *bolt.Tx) error {
		_, err := tx.CreateBucketIfNotExists(bucket)
		return err
	}); err != nil {
		db.Close()
		return nil, err
	}
	return &Store{db: db, salt: salt(apiKey), ttl: ttl}, nil
}

// salt keys entries to the credential that paid for them, so verdicts never
// cross accounts in a shared cache directory.
func salt(apiKey string) string {
	if apiKey == "" {
		return "anonymous"
	}
	sum := sha256.Sum256([]byte(apiKey))
	return hex.EncodeToString(sum[:8])
}

func (s *Store) key(id string) []byte { return []byte(s.salt + ":" + id) }

func (s *Store) Get(id string) (core.Verdict, bool) {
	var found entry
	err := s.db.View(func(tx *bolt.Tx) error {
		raw := tx.Bucket(bucket).Get(s.key(id))
		if raw == nil {
			return errors.New("miss")
		}
		return json.Unmarshal(raw, &found)
	})
	if err != nil {
		return core.Verdict{}, false
	}
	if s.ttl > 0 && time.Since(found.Stored) > s.ttl {
		return core.Verdict{}, false
	}
	return found.Verdict, true
}

func (s *Store) Put(id string, v core.Verdict) error {
	raw, err := json.Marshal(entry{Verdict: v, Stored: time.Now().UTC()})
	if err != nil {
		return err
	}
	return s.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket(bucket).Put(s.key(id), raw)
	})
}

func (s *Store) Stats() (count int, err error) {
	err = s.db.View(func(tx *bolt.Tx) error {
		count = tx.Bucket(bucket).Stats().KeyN
		return nil
	})
	return count, err
}

func (s *Store) Clear() error {
	return s.db.Update(func(tx *bolt.Tx) error {
		if err := tx.DeleteBucket(bucket); err != nil && !errors.Is(err, bolt.ErrBucketNotFound) {
			return err
		}
		_, err := tx.CreateBucketIfNotExists(bucket)
		return err
	})
}

func (s *Store) Close() error { return s.db.Close() }

// Noop is the store used with --no-cache: reads always miss, writes vanish.
type Noop struct{}

func (Noop) Get(string) (core.Verdict, bool) { return core.Verdict{}, false }
func (Noop) Put(string, core.Verdict) error  { return nil }
func (Noop) Close() error                    { return nil }
