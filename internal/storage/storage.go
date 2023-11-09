package storage

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/chelmertz/elly/internal/types"
)

func check(err error) {
	if err != nil {
		panic(err)
	}
}

type Storage struct {
	dirname  string
	filename string
	sync.Mutex
}

type StoredState struct {
	Prs         []types.ViewPr
	LastFetched time.Time
}

func NewStorage() *Storage {
	s := &Storage{}
	dirname, err := os.UserCacheDir()
	check(err)
	s.dirname = filepath.Join(dirname, "elly")
	if err := os.Mkdir(s.dirname, 0770); err != nil && !errors.Is(err, os.ErrExist) {
		check(err)
	}

	s.filename = filepath.Join(s.dirname, "prs.json")

	return s
}

func (s *Storage) Prs() StoredState {
	s.Lock()
	defer s.Unlock()

	oldContents, err := os.ReadFile(s.filename)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			oldContents = []byte("{}")
		} else {
			check(err)
		}
	}

	var prs_ = StoredState{}
	err = json.Unmarshal(oldContents, &prs_)
	check(err)

	return prs_
}

func (s *Storage) StoreRepoPrs(orderedPrs []types.ViewPr, logger *slog.Logger) error {
	s.Lock()
	defer s.Unlock()
	prs_ := StoredState{}
	prs_.Prs = make([]types.ViewPr, len(orderedPrs))
	copy(prs_.Prs, orderedPrs)
	prs_.LastFetched = time.Now()

	logger.Info("storing prs", slog.Int("prs", len(prs_.Prs)))

	newContents, err := json.Marshal(prs_)
	if err != nil {
		return fmt.Errorf("could not marshal json: %w", err)
	}

	if err := os.WriteFile(s.filename, newContents, 0660); err != nil {
		return fmt.Errorf("could not write file: %w", err)
	}

	return nil
}
