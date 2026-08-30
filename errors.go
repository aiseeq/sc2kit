package sc2kit

import (
	"errors"
	"strings"

	"github.com/aiseeq/sc2kit/api"
)

// ErrGameEnded is returned when the client refuses a request because the game
// (or replay) is already over. The protocol reports it as free-form text in
// Response.error, so the text is matched here, once, and callers use errors.Is.
var ErrGameEnded = errors.New("sc2kit: game has already ended")

// ProtocolError carries the protocol-level errors an SC2 response contained.
// Unwrap yields a sentinel (such as ErrGameEnded) for the conditions the
// library recognises, so callers never match on message text themselves.
type ProtocolError struct {
	Errors []string   // Response.error, verbatim
	Status api.Status // client status reported with the response
	known  error
}

func (e *ProtocolError) Error() string {
	return "sc2kit: response errors: " + strings.Join(e.Errors, "; ")
}

func (e *ProtocolError) Unwrap() error { return e.known }

// newProtocolError builds a ProtocolError, recognising the known conditions.
func newProtocolError(msgs []string, status api.Status) *ProtocolError {
	e := &ProtocolError{Errors: msgs, Status: status}
	joined := strings.ToLower(strings.Join(msgs, "; "))
	if strings.Contains(joined, "game has already ended") {
		e.known = ErrGameEnded
	}
	return e
}
