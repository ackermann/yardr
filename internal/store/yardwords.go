package store

import "strings"

// LoginEndedPrefix is the head of the words a harness login's ask is taken
// back with when the login ends without one (dispatch endLead): the rest is
// why, an error or the tail of the login command's output, which names files
// on the yard's machine. Bead and UserAsks tell the words to a device of the
// operator alone, and the sentence of the noted event that carries them is
// marked for WithoutHeld.
const LoginEndedPrefix = "the login ended: "

// fetchNotTaken and flowUnreadable are the yard's own sentences for a
// failure it writes onto a bead: a peer's returned branch it could not take
// (PeerFetchFailed), and a note from another yard for a bead whose flow does
// not read (peernote noteBack). Each carries the error after it, which names
// a file of the yard's: the worktree the branch is checked out in, git's
// stderr with the remote, or the depot's directory. HeldYardNote matches on
// these, the templates' fixed words around the error.
const (
	fetchNotTaken  = "this yard could not take the branch"
	flowUnreadable = "this bead's flow does not read"
)

// HeldYardNote is the text a device that is no operator's is told for the
// note of author with text, and whether it is held: the yard's own words
// about a failed fetch or an unreadable flow read as what happened, that the
// bead is held, and that why is an operator's to read, as Flows and
// DeliveryFiles say what failed and not why. Any other note, among them a
// person's and any other of the yard's, reads as it is: a path with words
// around it is still the path, so the whole text is held and nothing of it
// is kept. The socket, the terminal, the agents and the web view read the
// note whole from the store, as a device of an operator does.
func HeldYardNote(author, text string) (string, bool) {
	if author != "yard" {
		return "", false
	}
	switch {
	case strings.Contains(text, fetchNotTaken):
		return "A peer returned this bead with a branch this yard could not take, so the bead is held; why is an operator's to read.", true
	case strings.Contains(text, flowUnreadable):
		return "A note came from another yard and this bead's flow does not read, so the bead is held; why is an operator's to read.", true
	}
	return "", false
}

// HeldYardWords is the words a device that is no operator's is told for an
// ask taken back by by with words, and whether they are held: the words of a
// login ask taken back when the login ended read as that the login ended and
// that why is an operator's to read. Any other words, among them a person's,
// read as they are. The socket, the terminal, the agents and the web view
// read the words whole from the store, as a device of an operator does.
func HeldYardWords(by, words string) (string, bool) {
	if by != "yard" || !strings.HasPrefix(words, LoginEndedPrefix) {
		return "", false
	}
	return "the login ended; why is an operator's to read.", true
}
