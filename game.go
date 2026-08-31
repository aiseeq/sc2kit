package sc2kit

import (
	"context"
	"fmt"

	"github.com/aiseeq/sc2kit/api"
)

// CreateGame creates a game on the connected SC2 client. The client stays in
// the lobby until JoinGame is called.
func (c *Client) CreateGame(ctx context.Context, req *api.RequestCreateGame) error {
	resp, err := c.Request(ctx, &api.Request{Request: &api.Request_CreateGame{CreateGame: req}})
	if err != nil {
		return fmt.Errorf("create game: %w", err)
	}
	cg := resp.GetCreateGame()
	if cg == nil {
		return fmt.Errorf("create game: response is not a create-game response: %v", resp)
	}
	if cg.Error != nil {
		return fmt.Errorf("create game: %v: %v", cg.GetError(), cg.GetErrorDetails())
	}
	return nil
}

// JoinGame joins the created game and returns the assigned player id.
func (c *Client) JoinGame(ctx context.Context, req *api.RequestJoinGame) (uint32, error) {
	resp, err := c.Request(ctx, &api.Request{Request: &api.Request_JoinGame{JoinGame: req}})
	if err != nil {
		return 0, fmt.Errorf("join game: %w", err)
	}
	jg := resp.GetJoinGame()
	if jg == nil {
		return 0, fmt.Errorf("join game: response is not a join-game response: %v", resp)
	}
	if jg.Error != nil {
		return 0, fmt.Errorf("join game: %v: %v", jg.GetError(), jg.GetErrorDetails())
	}
	return jg.GetPlayerId(), nil
}

// StartReplay starts playback of a replay on the connected SC2 client.
// The replay only advances on Step requests: in the 4.10 client realtime=true
// does not move a replay. Replays reference the map by the path of the
// environment that recorded them, so set MapData to the map file contents to
// play a replay anywhere.
func (c *Client) StartReplay(ctx context.Context, req *api.RequestStartReplay) error {
	resp, err := c.Request(ctx, &api.Request{Request: &api.Request_StartReplay{StartReplay: req}})
	if err != nil {
		return fmt.Errorf("start replay: %w", err)
	}
	sr := resp.GetStartReplay()
	if sr == nil {
		return fmt.Errorf("start replay: response is not a start-replay response: %v", resp)
	}
	if sr.Error != nil {
		return fmt.Errorf("start replay: %v: %v", sr.GetError(), sr.GetErrorDetails())
	}
	return nil
}

// Observation requests the current game state.
func (c *Client) Observation(ctx context.Context, req *api.RequestObservation) (*api.ResponseObservation, error) {
	resp, err := c.Request(ctx, &api.Request{Request: &api.Request_Observation{Observation: req}})
	if err != nil {
		return nil, fmt.Errorf("observation: %w", err)
	}
	obs := resp.GetObservation()
	if obs == nil {
		return nil, fmt.Errorf("observation: response is not an observation response: %v", resp)
	}
	return obs, nil
}

// Step advances a non-realtime game by count game loops.
func (c *Client) Step(ctx context.Context, count uint32) (*api.ResponseStep, error) {
	resp, err := c.Request(ctx, &api.Request{Request: &api.Request_Step{Step: &api.RequestStep{Count: &count}}})
	if err != nil {
		return nil, fmt.Errorf("step: %w", err)
	}
	st := resp.GetStep()
	if st == nil {
		return nil, fmt.Errorf("step: response is not a step response: %v", resp)
	}
	return st, nil
}

// Action sends unit commands. The per-action results are returned so the
// caller can detect rejected commands.
func (c *Client) Action(ctx context.Context, actions []*api.Action) ([]api.ActionResult, error) {
	resp, err := c.Request(ctx, &api.Request{Request: &api.Request_Action{Action: &api.RequestAction{Actions: actions}}})
	if err != nil {
		return nil, fmt.Errorf("action: %w", err)
	}
	act := resp.GetAction()
	if act == nil {
		return nil, fmt.Errorf("action: response is not an action response: %v", resp)
	}
	return act.GetResult(), nil
}

// GameInfo requests static game information (map size, start locations, ...).
func (c *Client) GameInfo(ctx context.Context) (*api.ResponseGameInfo, error) {
	resp, err := c.Request(ctx, &api.Request{Request: &api.Request_GameInfo{GameInfo: &api.RequestGameInfo{}}})
	if err != nil {
		return nil, fmt.Errorf("game info: %w", err)
	}
	gi := resp.GetGameInfo()
	if gi == nil {
		return nil, fmt.Errorf("game info: response is not a game-info response: %v", resp)
	}
	return gi, nil
}

// Data requests static unit/ability/upgrade data.
func (c *Client) Data(ctx context.Context, req *api.RequestData) (*api.ResponseData, error) {
	resp, err := c.Request(ctx, &api.Request{Request: &api.Request_Data{Data: req}})
	if err != nil {
		return nil, fmt.Errorf("data: %w", err)
	}
	d := resp.GetData()
	if d == nil {
		return nil, fmt.Errorf("data: response is not a data response: %v", resp)
	}
	return d, nil
}

// Query asks the engine about placement, pathing and abilities: it answers the
// same checks the engine itself does, so a spot accepted here will not be
// silently dropped later.
func (c *Client) Query(ctx context.Context, req *api.RequestQuery) (*api.ResponseQuery, error) {
	resp, err := c.Request(ctx, &api.Request{Request: &api.Request_Query{Query: req}})
	if err != nil {
		return nil, fmt.Errorf("query: %w", err)
	}
	q := resp.GetQuery()
	if q == nil {
		return nil, fmt.Errorf("query: response is not a query response: %v", resp)
	}
	return q, nil
}

// Debug sends debug commands (create/kill units, set resources, ...).
func (c *Client) Debug(ctx context.Context, commands ...*api.DebugCommand) error {
	_, err := c.Request(ctx, &api.Request{Request: &api.Request_Debug{Debug: &api.RequestDebug{Debug: commands}}})
	if err != nil {
		return fmt.Errorf("debug: %w", err)
	}
	return nil
}

// SaveReplay returns the replay of the current/finished game as bytes.
func (c *Client) SaveReplay(ctx context.Context) ([]byte, error) {
	resp, err := c.Request(ctx, &api.Request{Request: &api.Request_SaveReplay{SaveReplay: &api.RequestSaveReplay{}}})
	if err != nil {
		return nil, fmt.Errorf("save replay: %w", err)
	}
	sr := resp.GetSaveReplay()
	if sr == nil {
		return nil, fmt.Errorf("save replay: response is not a save-replay response: %v", resp)
	}
	if len(sr.GetData()) == 0 {
		return nil, fmt.Errorf("save replay: empty replay data")
	}
	return sr.GetData(), nil
}

// LeaveGame surrenders/leaves the current game.
func (c *Client) LeaveGame(ctx context.Context) error {
	_, err := c.Request(ctx, &api.Request{Request: &api.Request_LeaveGame{LeaveGame: &api.RequestLeaveGame{}}})
	if err != nil {
		return fmt.Errorf("leave game: %w", err)
	}
	return nil
}

// Quit shuts the SC2 client process down.
func (c *Client) Quit(ctx context.Context) error {
	_, err := c.Request(ctx, &api.Request{Request: &api.Request_Quit{Quit: &api.RequestQuit{}}})
	if err != nil {
		return fmt.Errorf("quit: %w", err)
	}
	return nil
}
