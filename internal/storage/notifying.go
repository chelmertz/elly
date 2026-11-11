package storage

import (
	"time"

	"github.com/chelmertz/elly/internal/types"
)

// Notifying wraps s so that every write the page shows calls changed
// afterwards, failed or not: the page's change stream (server.Changes)
// re-renders on it. A spare re-render costs nothing; a missed one leaves
// the page stale, so TestEveryStorageMethodIsClassified fails on a Storage
// method that is in neither list.
func Notifying(s Storage, changed func()) Storage {
	return notifying{Storage: s, changed: changed}
}

type notifying struct {
	Storage
	changed func()
}

func (n notifying) StoreRepoPrs(prs []types.ViewPr) error {
	defer n.changed()
	return n.Storage.StoreRepoPrs(prs)
}

func (n notifying) Bury(prUrl string) error {
	defer n.changed()
	return n.Storage.Bury(prUrl)
}

func (n notifying) Unbury(prUrl string) error {
	defer n.changed()
	return n.Storage.Unbury(prUrl)
}

func (n notifying) SetRateLimitUntil(t time.Time) error {
	defer n.changed()
	return n.Storage.SetRateLimitUntil(t)
}

func (n notifying) StorePAT(token, username string, expiresAt time.Time) error {
	defer n.changed()
	return n.Storage.StorePAT(token, username, expiresAt)
}

func (n notifying) ClearPAT() error {
	defer n.changed()
	return n.Storage.ClearPAT()
}

func (n notifying) PutOwnerPAT(owner, token, username string, expiresAt time.Time) error {
	defer n.changed()
	return n.Storage.PutOwnerPAT(owner, token, username, expiresAt)
}

func (n notifying) DeleteOwnerPAT(owner string) (bool, error) {
	defer n.changed()
	return n.Storage.DeleteOwnerPAT(owner)
}

func (n notifying) SetOwnerPollState(o OwnerPAT) error {
	defer n.changed()
	return n.Storage.SetOwnerPollState(o)
}

func (n notifying) SetPATExpiry(token string, expiresAt time.Time) error {
	defer n.changed()
	return n.Storage.SetPATExpiry(token, expiresAt)
}

func (n notifying) SetOwnerPATExpiry(owner, token string, expiresAt time.Time) error {
	defer n.changed()
	return n.Storage.SetOwnerPATExpiry(owner, token, expiresAt)
}

func (n notifying) SetPATOwner(token, owner string) error {
	defer n.changed()
	return n.Storage.SetPATOwner(token, owner)
}

func (n notifying) StoreDegradations(ds []types.Degradation) error {
	defer n.changed()
	return n.Storage.StoreDegradations(ds)
}

func (n notifying) PutAnnotation(prUrl string, a types.Annotation, expiresAt time.Time) error {
	defer n.changed()
	return n.Storage.PutAnnotation(prUrl, a, expiresAt)
}

func (n notifying) DeleteAnnotation(prUrl, source string) error {
	defer n.changed()
	return n.Storage.DeleteAnnotation(prUrl, source)
}
