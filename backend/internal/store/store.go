// Package store persists the camera list, including credentials, to a JSON file.
package store

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// ErrNotFound is returned when no camera has the requested ID.
var ErrNotFound = errors.New("camera not found")

// Camera is one configured device. Pass is a plaintext camera password: the
// firmware only accepts the original password on its form login, so there is
// nothing to hash against. The file is written 0600 so it is readable only by
// the user running the NVR, and Pass is never included in API responses or logs.
type Camera struct {
	ID      string `json:"id"`
	Address string `json:"address"`
	Name    string `json:"name"`
	User    string `json:"user"`
	Pass    string `json:"pass"`
}

// Public is the credential-free view handed to API clients.
type Public struct {
	ID      string `json:"id"`
	Address string `json:"address"`
	Name    string `json:"name"`
	User    string `json:"user"`
}

// Public strips the password so a Camera can never be serialised to a client
// by accident.
func (c Camera) Public() Public {
	return Public{ID: c.ID, Address: c.Address, Name: c.Name, User: c.User}
}

// Host returns the address without any port, which is what mDNS results and
// the port 81 stream URL are matched and built against.
func (c Camera) Host() string {
	if h, _, err := net.SplitHostPort(c.Address); err == nil {
		return h
	}
	return c.Address
}

// Store is a concurrency-safe camera list backed by a JSON file.
type Store struct {
	mu   sync.RWMutex
	path string
	cams []Camera
}

// Open loads the camera list, treating a missing file as an empty list so a
// first run needs no setup.
func Open(path string) (*Store, error) {
	s := &Store{path: path}
	data, err := os.ReadFile(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return s, nil
	case err != nil:
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		return s, nil
	}
	if err := json.Unmarshal(data, &s.cams); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return s, nil
}

// List returns a copy so callers cannot mutate the store's slice.
func (s *Store) List() []Camera {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Camera, len(s.cams))
	copy(out, s.cams)
	return out
}

// Get returns the camera with the given ID.
func (s *Store) Get(id string) (Camera, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, c := range s.cams {
		if c.ID == id {
			return c, nil
		}
	}
	return Camera{}, ErrNotFound
}

// Add stores a new camera, assigning it an ID.
func (s *Store) Add(c Camera) (Camera, error) {
	if c.Address == "" {
		return Camera{}, errors.New("address is required")
	}
	if c.Name == "" {
		c.Name = c.Address
	}
	c.ID = newID()

	s.mu.Lock()
	defer s.mu.Unlock()
	s.cams = append(s.cams, c)
	if err := s.saveLocked(); err != nil {
		s.cams = s.cams[:len(s.cams)-1]
		return Camera{}, err
	}
	return c, nil
}

// SetAddress rewrites one camera's address, for when the address it was added
// under turns out not to be the one that resolves. It reports whether anything
// changed so callers can skip restarting a device that is already right.
func (s *Store) SetAddress(id, addr string) (bool, error) {
	if addr == "" {
		return false, errors.New("address is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, c := range s.cams {
		if c.ID != id {
			continue
		}
		if c.Address == addr {
			return false, nil
		}
		old := c.Address
		s.cams[i].Address = addr
		if err := s.saveLocked(); err != nil {
			s.cams[i].Address = old
			return false, err
		}
		return true, nil
	}
	return false, ErrNotFound
}

// Delete removes a camera by ID.
func (s *Store) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, c := range s.cams {
		if c.ID != id {
			continue
		}
		removed := s.cams
		s.cams = append(append([]Camera{}, s.cams[:i]...), s.cams[i+1:]...)
		if err := s.saveLocked(); err != nil {
			s.cams = removed
			return err
		}
		return nil
	}
	return ErrNotFound
}

// saveLocked writes through a temp file and renames, so a crash mid-write
// cannot leave a half-written camera list on disk.
func (s *Store) saveLocked() error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return fmt.Errorf("create data dir: %w", err)
	}
	data, err := json.MarshalIndent(s.cams, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(s.path), ".cameras-*.json")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())

	// 0600 because this file holds camera passwords in plaintext.
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), s.path)
}

func newID() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}
