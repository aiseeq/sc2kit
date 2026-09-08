package sc2kit

import (
	"errors"
	"strings"

	"github.com/aiseeq/sc2kit/api"
)

// ErrGameEnded is returned when the client refuses a request because the game
// (or replay) is already over. The protocol reports it as free-form text in
// Response.error, so the text is matched here, once, and callers use errors.Is.
// The two texts SC2 uses are the ones python-sc2 recognises
// (sc2/protocol.py ProtocolError.is_game_over_error): "Game has already
// ended" and "Not supported if game has already ended".
var ErrGameEnded = errors.New("sc2kit: game has already ended")

// ErrConnectionClosed is returned by every request after the websocket
// failed once: a request whose response was never read leaves the stream
// out of step, so the next read would return the previous answer. python-sc2
// marks the same state with ConnectionAlreadyClosed; go-sc2ai closes its
// request channel on the websocket close handler.
var ErrConnectionClosed = errors.New("sc2kit: connection closed")

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
	if strings.Contains(joined, "game has already ended") || strings.Contains(joined, "not supported if game has already ended") {
		e.known = ErrGameEnded
	}
	return e
}
