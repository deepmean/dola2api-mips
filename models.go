package main

import (
	"net/http"
	"strings"
)

// modelInfo describes one model this build can ask the upstream for.
//
// The shape follows the OpenAI listing so an existing client can read it, with
// the video-specific fields added rather than folded into a description string.
type modelInfo struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	OwnedBy string `json:"owned_by"`
	// Label is the name the web client shows for this model.
	Label string `json:"label,omitempty"`
	// Ratios and Durations are the values the service will accept. The API
	// validates the shape of both, so these are the documented choices rather
	// than a hard whitelist.
	Ratios    []string `json:"ratios,omitempty"`
	Durations []int    `json:"durations,omitempty"`
}

// modelListResponse is the body of GET /v1/models.
type modelListResponse struct {
	Object string      `json:"object"`
	Data   []modelInfo `json:"data"`
}

// defaultRatios and defaultDurations are the values the web client offers.
var (
	defaultRatios    = []string{"16:9", "9:16", "1:1", "4:3", "3:4", "21:9"}
	defaultDurations = []int{5, 10}
)

// knownModels is the list this build serves.
//
// The identifier is the value passed through as chat_ability.ability_param.model.
// It is configurable through DOLA_VIDEO_MODELS because the upstream's own
// identifier is not something this project can pin down from the outside: the
// web client builds its submit in a lazily loaded chunk, and the model name
// there is a display tag rather than necessarily the API value. Listing what
// the service will actually send is more useful than guessing, and an operator
// can correct it without a rebuild.
func (a *api) knownModels() []modelInfo {
	ids := a.cfg.VideoModels
	if len(ids) == 0 {
		ids = []string{defaultVideoModel}
	}
	out := make([]modelInfo, 0, len(ids))
	for _, id := range ids {
		out = append(out, modelInfo{
			ID:        id,
			Object:    "model",
			OwnedBy:   "dola",
			Label:     "Seedance 2.0",
			Ratios:    defaultRatios,
			Durations: defaultDurations,
		})
	}
	return out
}

// modelAllowed reports whether a requested model is one this build offers.
//
// An empty configured list means "accept anything", which is the honest
// behaviour for a configuration that has not been told otherwise. The loader
// always fills the list, so this only matters for a directly constructed Config.
func (a *api) modelAllowed(name string) bool {
	if len(a.cfg.VideoModels) == 0 {
		return true
	}
	for _, id := range a.cfg.VideoModels {
		if strings.EqualFold(id, name) {
			return true
		}
	}
	return false
}

// handleModels lists the models. It is authenticated like the rest of /v1: the
// list is not a secret, but leaving one route open is how a service ends up
// half-protected.
func (a *api) handleModels(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, modelListResponse{Object: "list", Data: a.knownModels()})
}
