package tg

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"io"
	"os"
	"path/filepath"

	"github.com/gotd/td/session"
)

// EncryptedStorage implements gotd's session.Storage, encrypting the session
// blob at rest with AES-256-GCM so that a leaked file cannot be replayed as
// the authenticated user.
type EncryptedStorage struct {
	path string
	aead cipher.AEAD
}

// NewEncryptedStorage builds a storage bound to path with the given 32-byte key.
func NewEncryptedStorage(path string, key []byte) (*EncryptedStorage, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &EncryptedStorage{path: path, aead: aead}, nil
}

func (s *EncryptedStorage) LoadSession(_ context.Context) ([]byte, error) {
	data, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, session.ErrNotFound
		}
		return nil, err
	}
	if len(data) < s.aead.NonceSize() {
		return nil, session.ErrNotFound
	}
	nonce, sealed := data[:s.aead.NonceSize()], data[s.aead.NonceSize():]
	plain, err := s.aead.Open(nil, nonce, sealed, nil)
	if err != nil {
		return nil, session.ErrNotFound
	}
	return plain, nil
}

func (s *EncryptedStorage) StoreSession(_ context.Context, data []byte) error {
	nonce := make([]byte, s.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	sealed := s.aead.Seal(nonce, nonce, data, nil)
	return os.WriteFile(s.path, sealed, 0o600)
}
