package server

import (
	"encoding/json"
	"io"
	"net/http"

	"github.com/sirily11/debate-bot/internal/config"
	"github.com/sirily11/debate-bot/internal/planner"
)

// planResponse is the body returned by POST /api/plan and /api/plan/improve:
// the structured script (for the dashboard's form/diagram), its rendered
// markdown, and whether live research backed the draft.
type planResponse struct {
	Script     *config.DebateTopic `json:"script"`
	Markdown   string              `json:"markdown"`
	Sources    []config.Source     `json:"sources,omitempty"`
	Researched bool                `json:"researched"`
}

// handlePlan drafts a fresh discussion script from a topic.
func (s *Server) handlePlan(w http.ResponseWriter, r *http.Request) {
	p, err := planner.New(s.plannerEnv())
	if err != nil {
		http.Error(w, "planning not available: "+err.Error(), http.StatusServiceUnavailable)
		return
	}
	var req planner.PlanRequest
	if !decodeJSONBody(w, r, &req) {
		return
	}
	res, err := p.Generate(r.Context(), req)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, planResponse{Script: res.Script, Markdown: res.Markdown, Sources: res.Sources, Researched: res.Researched})
}

// planImproveRequest carries the prior script plus the revision instruction
// and any uploaded reference files to ground the revision.
type planImproveRequest struct {
	PreviousScript *config.DebateTopic  `json:"previousScript"`
	Instruction    string               `json:"instruction"`
	Attachments    []planner.Attachment `json:"attachments,omitempty"`
}

// handlePlanImprove revises an existing script per a free-text instruction.
func (s *Server) handlePlanImprove(w http.ResponseWriter, r *http.Request) {
	p, err := planner.New(s.plannerEnv())
	if err != nil {
		http.Error(w, "planning not available: "+err.Error(), http.StatusServiceUnavailable)
		return
	}
	var req planImproveRequest
	if !decodeJSONBody(w, r, &req) {
		return
	}
	res, err := p.Improve(r.Context(), req.PreviousScript, req.Instruction, nil, req.Attachments)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, planResponse{Script: res.Script, Markdown: res.Markdown, Sources: res.Sources, Researched: res.Researched})
}

// decodeJSONBody reads + unmarshals a small JSON request body, writing a 400
// on failure. Returns false when the caller should stop (response already
// written).
func decodeJSONBody(w http.ResponseWriter, r *http.Request, v any) bool {
	defer r.Body.Close()
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		http.Error(w, "read body", http.StatusBadRequest)
		return false
	}
	if err := json.Unmarshal(body, v); err != nil {
		http.Error(w, "bad json: "+err.Error(), http.StatusBadRequest)
		return false
	}
	return true
}
