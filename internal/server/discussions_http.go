package server

import (
	"net/http"
	"strings"

	"github.com/sirily11/debate-bot/internal/planner"
)

type discussionImproveRequest struct {
	Instruction string `json:"instruction"`
}

type discussionGenerateRequest struct {
	VideoConfig videoConfigJSON `json:"videoConfig"`
	Language    string          `json:"language"`
}

func (s *Server) handleDiscussionList(w http.ResponseWriter, r *http.Request) {
	user := s.requestUser(r)
	items, err := s.d.Discussions.List(r.Context(), user.ID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	for i := range items {
		s.applyDiscussionJobStatus(r, &items[i])
	}
	writeJSON(w, items)
}

func (s *Server) handleDiscussionGet(w http.ResponseWriter, r *http.Request) {
	d := s.getOwnedDiscussion(w, r)
	if d == nil {
		return
	}
	s.applyDiscussionJobStatus(r, d)
	writeJSON(w, d)
}

func (s *Server) handleDiscussionPlan(w http.ResponseWriter, r *http.Request) {
	p, err := planner.New(s.d.Env)
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
	resp := planResponse{Script: res.Script, Markdown: res.Markdown, Sources: res.Sources, Researched: res.Researched}
	d, err := s.d.Discussions.Create(r.Context(), s.requestUser(r).ID, req.Topic, resp)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, d)
}

func (s *Server) handleDiscussionImprove(w http.ResponseWriter, r *http.Request) {
	user := s.requestUser(r)
	id := r.PathValue("id")
	d, err := s.d.Discussions.Get(r.Context(), user.ID, id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if d == nil || d.Script == nil {
		http.NotFound(w, r)
		return
	}
	var req discussionImproveRequest
	if !decodeJSONBody(w, r, &req) {
		return
	}
	instruction := strings.TrimSpace(req.Instruction)
	if instruction == "" {
		http.Error(w, "instruction is required", http.StatusBadRequest)
		return
	}
	p, err := planner.New(s.d.Env)
	if err != nil {
		http.Error(w, "planning not available: "+err.Error(), http.StatusServiceUnavailable)
		return
	}
	res, err := p.Improve(r.Context(), d.Script, instruction)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	_ = s.d.Discussions.AppendEditTurn(r.Context(), user.ID, id, "user", instruction)
	resp := planResponse{Script: res.Script, Markdown: res.Markdown, Sources: res.Sources, Researched: res.Researched}
	updated, err := s.d.Discussions.UpdatePlan(r.Context(), user.ID, id, resp)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	_ = s.d.Discussions.AppendEditTurn(r.Context(), user.ID, id, "plan", "Updated plan")
	writeJSON(w, updated)
}

func (s *Server) handleDiscussionGenerate(w http.ResponseWriter, r *http.Request) {
	user := s.requestUser(r)
	id := r.PathValue("id")
	d, err := s.d.Discussions.Get(r.Context(), user.ID, id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if d == nil || d.Script == nil {
		http.NotFound(w, r)
		return
	}
	var req discussionGenerateRequest
	if !decodeJSONBody(w, r, &req) {
		return
	}
	if lang := strings.TrimSpace(req.Language); lang != "" {
		next := *d.Script
		next.Language = lang
		md, err := next.RenderMarkdown()
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		sources := d.Sources
		if len(sources) == 0 {
			sources = next.Sources
		}
		updated, err := s.d.Discussions.UpdatePlan(r.Context(), user.ID, id, planResponse{
			Script:     &next,
			Markdown:   md,
			Sources:    sources,
			Researched: d.Researched,
		})
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if updated == nil || updated.Script == nil {
			http.NotFound(w, r)
			return
		}
		d = updated
	}
	jobID, err := s.submitJSONScript(d.Script, req.VideoConfig, id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	updated, err := s.d.Discussions.SetJob(r.Context(), user.ID, id, jobID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, updated)
}

func (s *Server) handleDiscussionAppendLine(w http.ResponseWriter, r *http.Request) {
	var line DiscussionLine
	if !decodeJSONBody(w, r, &line) {
		return
	}
	if err := s.d.Discussions.AppendLine(r.Context(), s.requestUser(r).ID, r.PathValue("id"), line); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleDiscussionDelete(w http.ResponseWriter, r *http.Request) {
	ok, err := s.d.Discussions.Delete(r.Context(), s.requestUser(r).ID, r.PathValue("id"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if !ok {
		http.NotFound(w, r)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) getOwnedDiscussion(w http.ResponseWriter, r *http.Request) *Discussion {
	d, err := s.d.Discussions.Get(r.Context(), s.requestUser(r).ID, r.PathValue("id"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return nil
	}
	if d == nil {
		http.NotFound(w, r)
		return nil
	}
	return d
}

func (s *Server) applyDiscussionJobStatus(r *http.Request, d *Discussion) {
	if d == nil || d.JobID == "" || s.d.Jobs == nil {
		return
	}
	j := s.d.Jobs.Get(d.JobID)
	if j == nil {
		j = s.recoverJob(d.JobID)
		if j == nil {
			if d.Status == DiscussionGenerating {
				d.Status = DiscussionFailed
				_ = s.d.Discussions.SetJobResult(r.Context(), d.ID, DiscussionFailed, d.DownloadURL)
			}
			return
		}
	}
	switch {
	case j.Status == JobDone:
		d.Status = DiscussionReady
		if url := s.jobDownloadURL(r.Context(), j); url != "" {
			d.DownloadURL = url
		} else if d.DownloadURL == "" && j.DownloadURL != "" {
			d.DownloadURL = j.DownloadURL
		}
		_ = s.d.Discussions.SetJobResult(r.Context(), d.ID, DiscussionReady, d.DownloadURL)
		if j.TotalTokens > 0 {
			d.PromptTokens = j.PromptTokens
			d.CompletionTokens = j.CompletionTokens
			d.TotalTokens = j.TotalTokens
			d.LLMCostUSD = j.LLMCostUSD
			d.LLMCostKnown = j.LLMCostKnown
			_ = s.d.Discussions.SetUsage(r.Context(), d.ID,
				j.PromptTokens, j.CompletionTokens, j.TotalTokens, j.LLMCostUSD, j.LLMCostKnown)
		}
	case j.Status == JobError:
		d.Status = DiscussionFailed
		_ = s.d.Discussions.SetJobResult(r.Context(), d.ID, DiscussionFailed, d.DownloadURL)
	}
}
