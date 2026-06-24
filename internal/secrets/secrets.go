// Package secrets manages a sealed secrets store for projx-engine.
//
// Plaintext never reaches the agent process. The agent sees only codenames via
// PROJX_SECRET_NAMES. Plaintext is injected into brokered child processes by the
// exec-jail shim at exec time.
//
// Storage layout: <dir>/key (32 raw bytes, AES-256 key) and <dir>/store.json
// (JSON map of codename -> base64(nonce||ciphertext)). Both files are 0600.
//
// Default dir: os.UserConfigDir()/projx/secrets. Override: PROJX_SECRETS_DIR.
package secrets

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

var codenameRE = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// Store is a sealed secrets store backed by AES-256-GCM.
type Store struct {
	dir    string
	key    []byte            // 32 bytes
	sealed map[string]string // codename -> base64(nonce||ciphertext)
}

// Open resolves the store directory, loads or creates the AES key, and reads
// existing sealed entries. Never returns plaintext.
func Open() (*Store, error) {
	dir := os.Getenv("PROJX_SECRETS_DIR")
	if dir == "" {
		cfgDir, err := os.UserConfigDir()
		if err != nil {
			return nil, fmt.Errorf("secrets: UserConfigDir: %w", err)
		}
		dir = filepath.Join(cfgDir, "projx", "secrets")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("secrets: mkdir %s: %w", dir, err)
	}
	key, err := loadOrCreateKey(filepath.Join(dir, "key"))
	if err != nil {
		return nil, err
	}
	sealed, err := loadStore(filepath.Join(dir, "store.json"))
	if err != nil {
		return nil, err
	}
	return &Store{dir: dir, key: key, sealed: sealed}, nil
}

func loadOrCreateKey(keyPath string) ([]byte, error) {
	data, err := os.ReadFile(keyPath)
	if err == nil && len(data) == 32 {
		return data, nil
	}
	key := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, key); err != nil {
		return nil, fmt.Errorf("secrets: generate key: %w", err)
	}
	if err := os.WriteFile(keyPath, key, 0o600); err != nil {
		return nil, fmt.Errorf("secrets: write key: %w", err)
	}
	return key, nil
}

func loadStore(storePath string) (map[string]string, error) {
	data, err := os.ReadFile(storePath)
	if os.IsNotExist(err) {
		return make(map[string]string), nil
	}
	if err != nil {
		return nil, fmt.Errorf("secrets: read store: %w", err)
	}
	var m map[string]string
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("secrets: parse store: %w", err)
	}
	return m, nil
}

func (s *Store) persist() error {
	data, err := json.MarshalIndent(s.sealed, "", "  ")
	if err != nil {
		return fmt.Errorf("secrets: marshal: %w", err)
	}
	return os.WriteFile(filepath.Join(s.dir, "store.json"), data, 0o600)
}

// Set seals value under codename (AES-256-GCM, random nonce) and persists.
// codename must match ^[A-Za-z_][A-Za-z0-9_]*$.
func (s *Store) Set(codename, value string) error {
	if !codenameRE.MatchString(codename) {
		return fmt.Errorf("secrets: invalid codename %q (must match ^[A-Za-z_][A-Za-z0-9_]*$)", codename)
	}
	block, err := aes.NewCipher(s.key)
	if err != nil {
		return fmt.Errorf("secrets: aes: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return fmt.Errorf("secrets: gcm: %w", err)
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return fmt.Errorf("secrets: nonce: %w", err)
	}
	// Seal appends ciphertext+tag to nonce so the blob is nonce||ciphertext.
	blob := gcm.Seal(nonce, nonce, []byte(value), nil)
	s.sealed[codename] = base64.StdEncoding.EncodeToString(blob)
	return s.persist()
}

// Names returns sorted codenames. Never returns values.
func (s *Store) Names() []string {
	names := make([]string, 0, len(s.sealed))
	for k := range s.sealed {
		names = append(names, k)
	}
	sort.Strings(names)
	return names
}

// Delete removes codename from the store.
func (s *Store) Delete(codename string) error {
	if _, ok := s.sealed[codename]; !ok {
		return fmt.Errorf("secrets: %q not found", codename)
	}
	delete(s.sealed, codename)
	return s.persist()
}

// Resolve decrypts all sealed entries and returns codename→plaintext.
// Called ONLY by the brokered-exec injector. Never call from agent context.
func (s *Store) Resolve() (map[string]string, error) {
	out := make(map[string]string, len(s.sealed))
	for name, b64 := range s.sealed {
		raw, err := base64.StdEncoding.DecodeString(b64)
		if err != nil {
			return nil, fmt.Errorf("secrets: decode %q: %w", name, err)
		}
		block, err := aes.NewCipher(s.key)
		if err != nil {
			return nil, fmt.Errorf("secrets: aes: %w", err)
		}
		gcm, err := cipher.NewGCM(block)
		if err != nil {
			return nil, fmt.Errorf("secrets: gcm: %w", err)
		}
		ns := gcm.NonceSize()
		if len(raw) < ns {
			return nil, fmt.Errorf("secrets: ciphertext too short for %q", name)
		}
		plain, err := gcm.Open(nil, raw[:ns], raw[ns:], nil)
		if err != nil {
			return nil, fmt.Errorf("secrets: decrypt %q: %w", name, err)
		}
		out[name] = string(plain)
	}
	return out, nil
}

// NamesFromEnv parses PROJX_SECRET_NAMES (comma-separated) from the environment.
func NamesFromEnv() []string {
	v := os.Getenv("PROJX_SECRET_NAMES")
	if v == "" {
		return nil
	}
	var names []string
	for _, s := range strings.Split(v, ",") {
		s = strings.TrimSpace(s)
		if s != "" {
			names = append(names, s)
		}
	}
	return names
}
