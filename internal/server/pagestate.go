package server

import "time"

// pageState is what the page as a whole has to say before any row: a page
// with nothing to list must say why, or "0 on you" reads as "all clear" on a
// page that has not read GitHub at all. It is a pure function of the token,
// the last poll and what that poll found, so every move between states is a
// change in one of those three:
//
//	no-token   --token saved-->        first-poll
//	first-poll --poll, no PRs-->       nothing
//	first-poll --poll, none on you-->  clear
//	first-poll --poll, some on you-->  list
//	nothing / clear / list  --next poll-->  whichever the new result is
//	any        --token removed-->      no-token
type pageState string

const (
	stateNoToken   pageState = "no-token"
	stateFirstPoll pageState = "first-poll"
	stateNothing   pageState = "nothing"
	stateClear     pageState = "clear"
	stateList      pageState = "list"
)

// pageStateOf derives the state. A token removed keeps no PRs (ClearPAT
// forgets them), but no-token wins regardless: without a token nothing shown
// could be current.
func pageStateOf(hasToken bool, lastFetched time.Time, prs, onYou int) pageState {
	switch {
	case !hasToken:
		return stateNoToken
	case lastFetched.IsZero():
		return stateFirstPoll
	case prs == 0:
		return stateNothing
	case onYou == 0:
		return stateClear
	default:
		return stateList
	}
}

// rowCount is every row on the page, buried and iced included.
func (d NextData) rowCount() int {
	n := 0
	for _, g := range d.Groups {
		n += len(g.Rows)
	}
	return n
}
