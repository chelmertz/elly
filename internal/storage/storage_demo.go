package storage

import (
	_ "embed"
	"time"

	"github.com/chelmertz/elly/internal/types"
	_ "modernc.org/sqlite"
)

type StorageDemo struct{}

var _ Storage = (*StorageDemo)(nil)

func NewStorageDemo() *StorageDemo {
	return &StorageDemo{}
}

func (s *StorageDemo) Prs() StoredState {
	prs := make([]types.ViewPr, 0)
	lastUpdated := time.Now().UTC()

	pr1 := types.ViewPr{
		Url:                      "1", // points is calculated based on PR URL, must be unique
		ReviewStatus:             "",
		Title:                    "feat: Scaffolding script for a new service",
		Author:                   "chelmertz",
		RepoName:                 "api",
		RepoOwner:                "chelmertz",
		RepoUrl:                  "",
		IsDraft:                  false,
		LastUpdated:              lastUpdated,
		LastPrCommenter:          "",
		ThreadsActionable:        3,
		ThreadsWaiting:           2,
		Additions:                32,
		Deletions:                15,
		ReviewRequestedFromUsers: []string{},
		Buried:                   false,
	}

	pr2 := types.ViewPr{
		Url:                      "2",
		ReviewStatus:             "",
		Title:                    "chore: update license",
		Author:                   "channy2011",
		RepoName:                 "infrastructure",
		RepoOwner:                "chelmertz",
		RepoUrl:                  "",
		IsDraft:                  true,
		LastUpdated:              lastUpdated,
		LastPrCommenter:          "",
		ThreadsActionable:        0,
		ThreadsWaiting:           0,
		Additions:                32,
		Deletions:                15,
		ReviewRequestedFromUsers: []string{},
		Buried:                   false,
	}

	pr3 := types.ViewPr{
		Url:                      "3",
		ReviewStatus:             "APPROVED",
		Title:                    "feature: add settings for maximum minutes of idling",
		Author:                   "bierden22",
		RepoName:                 "web",
		RepoOwner:                "chelmertz",
		RepoUrl:                  "",
		IsDraft:                  true,
		LastUpdated:              lastUpdated,
		LastPrCommenter:          "",
		ThreadsActionable:        0,
		ThreadsWaiting:           0,
		Additions:                32,
		Deletions:                15,
		ReviewRequestedFromUsers: []string{},
		Buried:                   false,
	}

	pr4 := types.ViewPr{
		Url:                      "4",
		ReviewStatus:             "APPROVED",
		Title:                    "style: fix linting errors",
		Author:                   "olsons_beats",
		RepoName:                 "translations",
		RepoOwner:                "chelmertz",
		RepoUrl:                  "",
		IsDraft:                  false,
		LastUpdated:              lastUpdated,
		LastPrCommenter:          "",
		ThreadsActionable:        0,
		ThreadsWaiting:           0,
		Additions:                32,
		Deletions:                15,
		ReviewRequestedFromUsers: []string{},
		Buried:                   true,
	}

	prs = append(prs, pr1, pr2, pr3, pr4)

	state := StoredState{
		Prs:         prs,
		LastFetched: time.Now().UTC(),
	}

	return state
}

func (s *StorageDemo) StoreRepoPrs(orderedPrs []types.ViewPr) error {
	return nil
}

func (s *StorageDemo) Bury(prUrl string) error {
	return nil
}

func (s *StorageDemo) Unbury(prUrl string) error {
	return nil
}

func (s *StorageDemo) GetPr(prUrl string) (Pr, error) {
	return Pr{}, nil
}

func (s *StorageDemo) SetRateLimitUntil(t time.Time) error {
	return nil
}

func (s *StorageDemo) IsRateLimitActive(now time.Time) bool {
	return false
}

func (s *StorageDemo) GetRateLimitUntil() time.Time {
	return time.Time{}
}

func (s *StorageDemo) StorePAT(token, username string, expiresAt time.Time) error {
	return nil
}

func (s *StorageDemo) GetPAT() (StoredPAT, bool, error) {
	return StoredPAT{
		Token:     "demo-token",
		Username:  "demo-user",
		SetAt:     time.Now().Add(-24 * time.Hour),
		ExpiresAt: time.Now().Add(30 * 24 * time.Hour),
	}, true, nil
}

func (s *StorageDemo) ClearPAT() error {
	return nil
}
