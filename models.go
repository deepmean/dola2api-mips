package main

import (
	"net/http"
	"strings"
)

// modelInfo describes one model this build can ask the upstream for.
//
// The shape follows the OpenAI listing so an existing client can read it, with
// the service-specific fields added rather than folded into a description
// string.
type modelInfo struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	OwnedBy string `json:"owned_by"`
	// Kind is "video", "image" or "chat". A client picks a model by id and has
	// no other way to know which endpoint that id belongs to, so the listing has
	// to say: an id that looks selectable but is answered with a 404 is worse
	// than one that is not offered.
	Kind string `json:"kind"`
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
// The video identifier is the value passed through as
// chat_ability.ability_param.model, and it is configurable through
// DOLA_VIDEO_MODELS because the upstream's own identifier is not something this
// project can pin down from the outside: the web client builds its submit in a
// lazily loaded chunk, and the name there is a display tag rather than
// necessarily the API value. Listing what the service will actually send is more
// useful than guessing, and an operator can correct it without a rebuild.
//
// The image and chat entries are this service's own names. They exist because a
// client needs an id to select: without one, a chat-capable service whose
// listing offers only a video model looks like it cannot chat at all.
func (a *api) knownModels() []modelInfo {
	video := a.cfg.VideoModels
	if len(video) == 0 {
		video = []string{defaultVideoModel}
	}
	out := make([]modelInfo, 0, len(video)+2)
	for _, id := range video {
		out = append(out, modelInfo{
			ID: id, Object: "model", OwnedBy: "dola", Kind: "video",
			Label: "Seedance 2.0", Ratios: defaultRatios, Durations: defaultDurations,
		})
	}

	images := a.cfg.ImageModels
	if len(images) == 0 {
		images = []string{defaultImageModel}
	}
	for _, id := range images {
		out = append(out, modelInfo{
			ID: id, Object: "model", OwnedBy: "dola", Kind: "image",
			Label: "Dola 圖片生成", Ratios: defaultRatios,
		})
	}

	chats := a.cfg.ChatModels
	if len(chats) == 0 {
		chats = []string{defaultChatModel}
	}
	for _, id := range chats {
		out = append(out, modelInfo{
			ID: id, Object: "model", OwnedBy: "dola", Kind: "chat", Label: "Dola Chat",
		})
	}
	return out
}

// modelAllowed reports whether a requested model is one this build offers for a
// kind of request.
//
// An empty configured list means "accept anything", which is the honest
// behaviour for a configuration that has not been told otherwise. The loader
// always fills the lists, so this only matters for a directly constructed
// Config.
func (a *api) modelAllowed(kind, name string) bool {
	var allowed []string
	switch kind {
	case "image":
		allowed = a.cfg.ImageModels
	case "chat":
		allowed = a.cfg.ChatModels
	default:
		allowed = a.cfg.VideoModels
	}
	if len(allowed) == 0 {
		return true
	}
	for _, id := range allowed {
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
