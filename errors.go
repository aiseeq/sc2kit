package sc2kit

import (
	"errors"
	"strings"

	"github.com/aiseeq/sc2kit/api"
)

// ErrGameEnded marks a protocol response saying the game or replay has ended.
// It is recognized from free-form Response.error text, so callers use errors.Is
// instead of matching the message themselves. Transport failures, including
// socket closure after Status_ended, do not produce ErrGameEnded.
var ErrGameEnded = errors.New("sc2kit: game has already ended")

// ErrConnectionClosed is returned by every request after the websocket
// failed once: a request whose response was never read leaves the stream
// out of step, so the next read would return the previous answer. python-sc2
// marks the same state with ConnectionAlreadyClosed; go-sc2ai closes its
// request channel on the websocket close handler.
var ErrConnectionClosed = errors.New("sc2kit: connection closed")

// ProtocolError carries protocol errors from a response without a command
// result, and recognized protocol conditions even when a command result is
// present. Unrecognized messages accompanying a result are returned as
// warnings. Unwrap yields a sentinel (such as ErrGameEnded) for known
// conditions, so callers never match on message text themselves.
type ProtocolError struct {
	Errors []string   // Response.error, verbatim
	Status api.Status // client status reported with the response
	known  error
}

type responseReadError struct{ err error }

func (e *responseReadError) Error() string { return e.err.Error() }

func (e *responseReadError) Unwrap() error { return e.err }

type requestDeadlineError struct{ err error }

func (e *requestDeadlineError) Error() string { return e.err.Error() }

func (e *requestDeadlineError) Unwrap() error { return e.err }

func (e *ProtocolError) Error() string {
	return "sc2kit: response errors: " + strings.Join(e.Errors, "; ")
}

func (e *ProtocolError) Unwrap() error { return e.known }

// newProtocolError builds a ProtocolError, recognising the known conditions.
func newProtocolError(msgs []string, status api.Status) *ProtocolError {
	return &ProtocolError{Errors: msgs, Status: status, known: knownProtocolError(msgs)}
}

func knownProtocolError(msgs []string) error {
	joined := strings.ToLower(strings.Join(msgs, "; "))
	if strings.Contains(joined, "game has already ended") || strings.Contains(joined, "not supported if game has already ended") {
		return ErrGameEnded
	}
	return nil
}
