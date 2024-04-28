package main

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/gorilla/schema"
	"golang.org/x/time/rate"
	"libdb.so/go-openshock"
)

//go:embed pages/*.html
var htmlPages embed.FS

var tmplPages = template.Must(template.
	New("").
	Funcs(template.FuncMap{
		"concatLines": func(lines []string) string { return strings.Join(lines, "\n") },
	}).
	ParseFS(htmlPages, "pages/*.html"))

type handler struct {
	state  *State
	client *openshock.Client
	rate   *rate.Limiter
	config Config
}

func newHandler(config Config, state *State) (http.Handler, error) {
	client, err := openshock.NewClient(config.OpenshockAPIServer, openshock.APIToken(config.OpenshockAPIToken))
	if err != nil {
		return nil, fmt.Errorf("failed to create OpenShock client: %w", err)
	}

	h := &handler{
		state:  state,
		client: client,
		rate:   rate.NewLimiter(0, 0),
		config: config,
	}

	r := chi.NewMux()
	r.Route("/admin", func(r chi.Router) {
		r.Get("/", h.adminIndex)
		r.Group(func(r chi.Router) {
			r.Use(parseForm)
			r.Post("/apply", h.adminApply)
			r.Post("/pause", h.adminPause)
		})
	})
	r.Post("/command/{token}", h.handle)

	return r, nil
}

type webhookRequest struct {
	AuthorUser string `json:"author_user"`
	TargetUser string `json:"target_user"`
	RoomID     string `json:"room_id,omitempty"`
	Command    string `json:"command"`
}

type webhookResponse struct {
	Message *string `json:"message"`
}

func (h *handler) handle(w http.ResponseWriter, r *http.Request) {
	token := chi.URLParam(r, "token")
	if token != h.config.ShockhookSecret {
		http.Error(w, "invalid token", http.StatusUnauthorized)
		return
	}

	var req webhookRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	slog.Info(
		"received webhook command",
		"author_user", req.AuthorUser,
		"target_user", req.TargetUser,
		"room_id", req.RoomID,
		"command", req.Command)

	resp := h.run(r.Context(), req)

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)

		slog.Error(
			"failed to encode webhook response",
			"err", err)
	}
}

func (h *handler) run(ctx context.Context, req webhookRequest) (resp webhookResponse) {
	state := h.state.Load()
	if state.AllowedRooms != nil && !slices.Contains(state.AllowedRooms, req.RoomID) {
		return
	}

	var control openshock.ControlType

	switch {
	case strings.HasPrefix(req.Command, "shock"), strings.HasPrefix(req.Command, "zap"):
		if !state.AllowShock {
			return
		}
		control = openshock.ControlTypeShock

	case strings.HasPrefix(req.Command, "vibrate"):
		if !state.AllowVibrate {
			return
		}
		control = openshock.ControlTypeVibrate

	default:
		return
	}

	if !h.state.Rate.Allow() {
		resp.Message = ptr("ow :< don't hurt it too much...")
		return
	}

	slog.Info(
		"sending control request",
		"author_user", req.AuthorUser,
		"target_user", req.TargetUser,
		"room_id", req.RoomID,
		"control", control)

	_, err := h.client.ShockerSendControl(ctx, openshock.NewOptControlRequest(openshock.ControlRequest{
		Shocks: openshock.NewOptNilControlArray([]openshock.Control{
			{
				ID:        openshock.NewOptUUID(h.config.OpenshockShockerID.AsUUID()),
				Type:      openshock.NewOptControlType(control),
				Intensity: openshock.NewOptInt32(int32(state.Intensity)),
				Duration:  openshock.NewOptInt32(int32(state.Duration.Milliseconds())),
			},
		}),
	}))
	if err != nil {
		slog.Error(
			"failed to send control request",
			"err", err)

		resp.Message = ptr("something went wrong :<")
		return
	}

	return
}

func ptr[T any](v T) *T { return &v }

func (h *handler) adminIndex(w http.ResponseWriter, r *http.Request) {
	state := h.state.Load()

	if err := tmplPages.ExecuteTemplate(w, "admin.html", state); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (h *handler) adminApply(w http.ResponseWriter, r *http.Request) {
	var form struct {
		Intensity    int     `schema:"intensity"`
		Duration     float64 `schema:"duration"`
		RateInterval float64 `schema:"rate_interval"`
		RateBurst    int     `schema:"rate_burst"`
		AllowShock   bool    `schema:"allow_shock"`
		AllowVibrate bool    `schema:"allow_vibrate"`
		AllowedRooms string  `schema:"allowed_rooms"`
	}

	if err := schemaDecoder.Decode(&form, r.Form); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	var allowedRooms []string
	if form.AllowedRooms != "*" {
		if form.AllowedRooms == "" {
			allowedRooms = []string{}
		} else {
			allowedRooms = strings.Split(form.AllowedRooms, "\n")
		}
	}

	newState := state{
		Paused:       h.state.Load().Paused,
		Intensity:    form.Intensity,
		Duration:     time.Duration(form.Duration * float64(time.Second)),
		RateInterval: time.Duration(form.RateInterval * float64(time.Second)),
		RateBurst:    form.RateBurst,
		AllowShock:   form.AllowShock,
		AllowVibrate: form.AllowVibrate,
		AllowedRooms: allowedRooms,
	}

	slog.Info(
		"applying new state",
		"state", newState)

	h.state.Store(newState)
	http.Redirect(w, r, "/admin", http.StatusSeeOther)
}

func (h *handler) adminPause(w http.ResponseWriter, r *http.Request) {
	var form struct {
		Paused bool `schema:"paused"`
	}
	if err := schemaDecoder.Decode(&form, r.Form); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	newState := h.state.Load()
	newState.Paused = form.Paused

	slog.Info(
		"toggling pause",
		"state", newState)

	h.state.Store(newState)
	http.Redirect(w, r, "/admin", http.StatusSeeOther)
}

var schemaDecoder = schema.NewDecoder()

func parseForm(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		next.ServeHTTP(w, r)
	})
}
