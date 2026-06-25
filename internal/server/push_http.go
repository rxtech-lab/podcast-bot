package server

import (
	"net/http"
)

type pushTokenRequest struct {
	Token       string `json:"token"`
	Environment string `json:"environment"`
	Platform    string `json:"platform"`
}

func (s *Server) handlePushTokenRegister(w http.ResponseWriter, r *http.Request) {
	var req pushTokenRequest
	if !decodeJSONBody(w, r, &req) {
		return
	}
	if req.Token == "" {
		http.Error(w, "token is required", http.StatusBadRequest)
		return
	}
	user := s.requestUser(r)
	environment := normalizePushEnvironment(req.Environment)
	token := normalizePushToken(req.Token)
	if err := s.d.Discussions.UpsertPushToken(
		r.Context(),
		user.ID,
		token,
		environment,
		req.Platform,
	); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.logger().Info("push token registered",
		"user", user.ID,
		"environment", environment,
		"platform", req.Platform,
		"token", maskPushToken(token))
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handlePushTokenDelete(w http.ResponseWriter, r *http.Request) {
	var req pushTokenRequest
	if !decodeJSONBody(w, r, &req) {
		return
	}
	user := s.requestUser(r)
	environment := normalizePushEnvironment(req.Environment)
	token := normalizePushToken(req.Token)
	if err := s.d.Discussions.DeletePushToken(
		r.Context(),
		user.ID,
		token,
		environment,
	); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.logger().Info("push token deleted",
		"user", user.ID,
		"environment", environment,
		"token", maskPushToken(token))
	w.WriteHeader(http.StatusNoContent)
}
