package sc2kit

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/aiseeq/sc2kit/api"
)

// gameSC2 plays a game in progress: every answer is derived from the
// request, so a test sees both what the method sent and what it returned.
func gameSC2(req *api.Request) answer {
	r := &api.Response{Status: api.Status_in_game.Enum()}
	switch {
	case req.GetCreateGame() != nil:
		cg := &api.ResponseCreateGame{}
		if req.GetCreateGame().GetLocalMap().GetMapPath() != "Fake LE.SC2Map" {
			cg.Error = api.ResponseCreateGame_InvalidMapPath.Enum()
			cg.ErrorDetails = new("no such map")
		}
		r.Response = &api.Response_CreateGame{CreateGame: cg}
	case req.GetJoinGame() != nil:
		jg := &api.ResponseJoinGame{PlayerId: new(uint32(2))}
		if req.GetJoinGame().GetRace() == api.Race_NoRace {
			jg = &api.ResponseJoinGame{Error: api.ResponseJoinGame_MissingParticipation.Enum()}
		}
		r.Response = &api.Response_JoinGame{JoinGame: jg}
	case req.GetObservation() != nil:
		r.Response = &api.Response_Observation{Observation: &api.ResponseObservation{
			Observation: &api.Observation{GameLoop: new(req.GetObservation().GetGameLoop() + 1)},
		}}
	case req.GetStep() != nil:
		if req.GetStep().GetCount() > 1000 {
			return answer{resp: &api.Response{Error: []string{"Game has already ended"}, Status: api.Status_ended.Enum()}}
		}
		r.Response = &api.Response_Step{Step: &api.ResponseStep{SimulationLoop: new(10 * req.GetStep().GetCount())}}
	case req.GetAction() != nil:
		var res []api.ActionResult
		for _, a := range req.GetAction().GetActions() {
			if a.GetActionChat().GetMessage() == "ok" {
				res = append(res, api.ActionResult_Success)
			} else {
				res = append(res, api.ActionResult_NotEnoughMinerals)
			}
		}
		r.Response = &api.Response_Action{Action: &api.ResponseAction{Result: res}}
	case req.GetGameInfo() != nil:
		r.Response = &api.Response_GameInfo{GameInfo: &api.ResponseGameInfo{MapName: new("Fake LE")}}
	case req.GetData() != nil:
		d := &api.ResponseData{}
		if req.GetData().GetUnitTypeId() {
			d.Units = []*api.UnitTypeData{{UnitId: new(uint32(84)), Name: new("Probe")}}
		}
		r.Response = &api.Response_Data{Data: d}
	case req.GetQuery() != nil:
		q := &api.ResponseQuery{}
		for _, p := range req.GetQuery().GetPlacements() {
			res := api.ActionResult_CantBuildLocationInvalid
			if p.GetAbilityId() == 1 {
				res = api.ActionResult_Success
			}
			q.Placements = append(q.Placements, &api.ResponseQueryBuildingPlacement{Result: res.Enum()})
		}
		r.Response = &api.Response_Query{Query: q}
	case req.GetDebug() != nil:
		if n := len(req.GetDebug().GetDebug()); n != 2 {
			return answer{resp: &api.Response{Error: []string{"want 2 debug commands"}}}
		}
		r.Response = &api.Response_Debug{Debug: &api.ResponseDebug{}}
	case req.GetSaveReplay() != nil:
		r.Response = &api.Response_SaveReplay{SaveReplay: &api.ResponseSaveReplay{Data: []byte("replay-bytes")}}
	default:
		return answer{resp: &api.Response{Error: []string{"unsupported request"}}}
	}
	return answer{resp: r}
}

func chat(msg string) *api.Action {
	return &api.Action{ActionChat: &api.ActionChat{Message: new(msg)}}
}

// The typed methods are what sc2go and sc2demu call: each sends its own
// request kind with the caller's fields and hands back what SC2 answered.
func TestSessionMethods(t *testing.T) {
	c, ctx := dialSC2(t, serveSC2(t, gameSC2))

	fake := &api.RequestCreateGame{Map: &api.RequestCreateGame_LocalMap{LocalMap: &api.LocalMap{MapPath: new("Fake LE.SC2Map")}}}
	if err := c.CreateGame(ctx, fake); err != nil {
		t.Errorf("CreateGame: %v", err)
	}
	missing := &api.RequestCreateGame{Map: &api.RequestCreateGame_LocalMap{LocalMap: &api.LocalMap{MapPath: new("Nope.SC2Map")}}}
	if err := c.CreateGame(ctx, missing); err == nil || !strings.Contains(err.Error(), "InvalidMapPath") {
		t.Errorf("CreateGame on a missing map = %v, want the InvalidMapPath code", err)
	}

	if id, err := c.JoinGame(ctx, &api.RequestJoinGame{Participation: &api.RequestJoinGame_Race{Race: api.Race_Zerg}}); err != nil || id != 2 {
		t.Errorf("JoinGame = %d, %v; want player 2", id, err)
	}
	if _, err := c.JoinGame(ctx, &api.RequestJoinGame{}); err == nil || !strings.Contains(err.Error(), "MissingParticipation") {
		t.Errorf("JoinGame without a race = %v, want the MissingParticipation code", err)
	}

	if obs, err := c.Observation(ctx, &api.RequestObservation{GameLoop: new(uint32(41))}); err != nil || obs.GetObservation().GetGameLoop() != 42 {
		t.Errorf("Observation = loop %d, %v; want loop 42", obs.GetObservation().GetGameLoop(), err)
	}
	if st, err := c.Step(ctx, 8); err != nil || st.GetSimulationLoop() != 80 {
		t.Errorf("Step(8) = loop %d, %v; want 80 (count 8 sent)", st.GetSimulationLoop(), err)
	}

	// sc2go matches results to its commands by index (bot/game/orders.go).
	res, err := c.Action(ctx, []*api.Action{chat("ok"), chat("broke"), chat("ok")})
	want := []api.ActionResult{api.ActionResult_Success, api.ActionResult_NotEnoughMinerals, api.ActionResult_Success}
	if err != nil || !slices.Equal(res, want) {
		t.Errorf("Action = %v, %v; want %v in command order", res, err, want)
	}

	if gi, err := c.GameInfo(ctx); err != nil || gi.GetMapName() != "Fake LE" {
		t.Errorf("GameInfo = %q, %v; want Fake LE", gi.GetMapName(), err)
	}
	if d, err := c.Data(ctx, &api.RequestData{UnitTypeId: new(true)}); err != nil || len(d.GetUnits()) != 1 || d.GetUnits()[0].GetName() != "Probe" {
		t.Errorf("Data(units) = %v, %v; want the Probe entry", d.GetUnits(), err)
	}

	q, err := c.Query(ctx, &api.RequestQuery{Placements: []*api.RequestQueryBuildingPlacement{{AbilityId: new(int32(1))}, {AbilityId: new(int32(2))}}})
	if err != nil || len(q.GetPlacements()) != 2 ||
		q.GetPlacements()[0].GetResult() != api.ActionResult_Success || q.GetPlacements()[1].GetResult() != api.ActionResult_CantBuildLocationInvalid {
		t.Errorf("Query = %v, %v; want [Success CantBuildLocationInvalid]", q.GetPlacements(), err)
	}

	end := &api.DebugCommand{Command: &api.DebugCommand_EndGame{EndGame: &api.DebugEndGame{}}}
	if err := c.Debug(ctx, end, end); err != nil {
		t.Errorf("Debug(2 commands): %v", err)
	}
	if err := c.Debug(ctx, end); err == nil {
		t.Error("Debug: protocol error from SC2 swallowed")
	}

	if rep, err := c.SaveReplay(ctx); err != nil || string(rep) != "replay-bytes" {
		t.Errorf("SaveReplay = %q, %v; want replay-bytes", rep, err)
	}
}

// Past the end of a replay SC2 answers Step with "Game has already ended":
// replay walkers (sc2go internal/replay.Walk, sc2demu corpus) stop on
// ErrGameEnded and read Status() to see the game is over.
func TestStepAfterGameEnded(t *testing.T) {
	c, ctx := dialSC2(t, serveSC2(t, gameSC2))

	_, err := c.Step(ctx, 5000)
	if !errors.Is(err, ErrGameEnded) {
		t.Errorf("Step past the end = %v, want ErrGameEnded", err)
	}
	if got := c.Status(); got != api.Status_ended {
		t.Errorf("Status after the end = %v, want ended", got)
	}
}

// A response of the wrong kind is an error, never a nil payload with a nil
// error that the caller would dereference.
func TestWrongResponseKind(t *testing.T) {
	c, ctx := dialSC2(t, serveSC2(t, func(req *api.Request) answer {
		if req.GetPing() != nil {
			return answer{resp: &api.Response{Response: &api.Response_Step{Step: &api.ResponseStep{}}}}
		}
		return answer{resp: &api.Response{Response: &api.Response_Ping{Ping: &api.ResponsePing{}}}}
	}))

	calls := map[string]func() error{
		"Ping":        func() error { _, err := c.Ping(ctx); return err },
		"CreateGame":  func() error { return c.CreateGame(ctx, &api.RequestCreateGame{}) },
		"JoinGame":    func() error { _, err := c.JoinGame(ctx, &api.RequestJoinGame{}); return err },
		"StartReplay": func() error { return c.StartReplay(ctx, &api.RequestStartReplay{}) },
		"Observation": func() error { _, err := c.Observation(ctx, &api.RequestObservation{}); return err },
		"Step":        func() error { _, err := c.Step(ctx, 1); return err },
		"Action":      func() error { _, err := c.Action(ctx, nil); return err },
		"GameInfo":    func() error { _, err := c.GameInfo(ctx); return err },
		"Data":        func() error { _, err := c.Data(ctx, &api.RequestData{}); return err },
		"Query":       func() error { _, err := c.Query(ctx, &api.RequestQuery{}); return err },
		"SaveReplay":  func() error { _, err := c.SaveReplay(ctx); return err },
	}
	for name, call := range calls {
		if err := call(); err == nil || !strings.Contains(err.Error(), "response is not") {
			t.Errorf("%s on a response of another kind = %v, want a wrong-kind error", name, err)
		}
	}
}
