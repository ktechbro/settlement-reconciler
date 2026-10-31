package reconcile

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type Principal struct {
	Tenant string `json:"tenant_id"`
	Actor  string `json:"actor"`
}
type Auth map[[32]byte]Principal

func NewAuth(tokens map[string]Principal) (Auth, error) {
	result := Auth{}
	if len(tokens) == 0 {
		return nil, errors.New("at least one tenant token required")
	}
	for token, p := range tokens {
		if len(token) < 24 || !identifier.MatchString(p.Tenant) || !identifier.MatchString(p.Actor) {
			return nil, errors.New("tenant tokens must have at least 24 characters and valid tenant/actor IDs")
		}
		result[sha256.Sum256([]byte(token))] = p
	}
	return result, nil
}
func (a Auth) Tenants() []string {
	seen := map[string]bool{}
	result := []string{}
	for _, p := range a {
		if !seen[p.Tenant] {
			seen[p.Tenant] = true
			result = append(result, p.Tenant)
		}
	}
	return result
}
func respond(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func failure(w http.ResponseWriter, err error) {
	status := 503
	message := "storage operation unavailable"
	if errors.Is(err, ErrConflict) {
		status = 409
		message = ErrConflict.Error()
	} else if errors.Is(err, ErrNotFound) {
		status = 404
		message = "not found"
	} else if errors.Is(err, ErrInvalid) {
		status = 422
		message = err.Error()
	}
	respond(w, status, map[string]string{"error": message})
}

func Handler(service *Service, auth Auth) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health/live", func(w http.ResponseWriter, r *http.Request) { respond(w, 200, map[string]string{"status": "alive"}) })
	mux.HandleFunc("GET /health/ready", func(w http.ResponseWriter, r *http.Request) {
		if err := service.Store.Pool.Ping(r.Context()); err != nil {
			respond(w, 503, map[string]string{"status": "database unavailable"})
			return
		}
		respond(w, 200, map[string]string{"status": "ready"})
	})
	protected := func(next func(http.ResponseWriter, *http.Request, Principal)) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
			p, ok := auth[sha256.Sum256([]byte(token))]
			if !ok || !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
				respond(w, 401, map[string]string{"error": "valid bearer token required"})
				return
			}
			next(w, r, p)
		}
	}
	for _, kind := range []string{"decisions", "mappings", "payment-attempts", "settlements", "coverage", "reconcile", "resolutions", "escrow-deals", "escrow-events"} {
		handler := protected(func(w http.ResponseWriter, r *http.Request, p Principal) {
			raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
			if err != nil {
				respond(w, 413, map[string]string{"error": "request body too large"})
				return
			}
			if !json.Valid(raw) {
				respond(w, 400, map[string]string{"error": "invalid JSON"})
				return
			}
			if pathKey := r.PathValue("id"); pathKey != "" {
				var input ResolutionInput
				if strictDecode(raw, &input) != nil || input.CaseKey != pathKey {
					respond(w, 422, map[string]string{"error": "resolution case_key must equal the path key"})
					return
				}
			}
			response, replayed, err := service.Write(r.Context(), p.Tenant, p.Actor, kind, r.Header.Get("Idempotency-Key"), raw)
			if err != nil {
				failure(w, err)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			if replayed {
				w.Header().Set("Idempotency-Replayed", "true")
			}
			w.WriteHeader(201)
			_, _ = w.Write(response)
		})
		mux.HandleFunc("POST /v1/"+kind, handler)
		if kind == "resolutions" {
			mux.HandleFunc("POST /v1/reconciliation/cases/{id}/resolutions", handler)
		}
	}
	mux.HandleFunc("GET /v1/reconciliation/cases", protected(func(w http.ResponseWriter, r *http.Request, p Principal) {
		limit := 100
		if value := r.URL.Query().Get("limit"); value != "" {
			n, err := strconv.Atoi(value)
			if err != nil || n < 1 || n > 500 {
				respond(w, 400, map[string]string{"error": "limit must be 1..500"})
				return
			}
			limit = n
		}
		after := r.URL.Query().Get("cursor")
		values, err := service.Store.Cases(r.Context(), p.Tenant, r.URL.Query().Get("status"), after, limit)
		if err != nil {
			failure(w, err)
			return
		}
		next := after
		if len(values) > 0 {
			next = values[len(values)-1].Case.Key
		}
		respond(w, 200, map[string]any{"data": values, "next_cursor": next})
	}))
	mux.HandleFunc("GET /v1/decisions/{id}/evidence", protected(func(w http.ResponseWriter, r *http.Request, p Principal) {
		value, err := service.Store.Trace(r.Context(), p.Tenant, r.PathValue("id"))
		if err != nil {
			failure(w, err)
			return
		}
		respond(w, 200, value)
	}))
	mux.HandleFunc("GET /v1/escrows/{id}/evidence", protected(func(w http.ResponseWriter, r *http.Request, p Principal) {
		value, err := service.Store.EscrowTrace(r.Context(), p.Tenant, r.PathValue("id"))
		if err != nil {
			failure(w, err)
			return
		}
		respond(w, 200, value)
	}))
	mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		respond(w, 200, map[string]any{"service": "Settlement Reconciler", "rule_version": RuleVersion, "docs": "https://github.com/ktechbro/settlement-reconciler", "authentication": "Bearer token; tenant comes from configured credential, never request data"})
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Cache-Control", "no-store")
		ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
		defer cancel()
		mux.ServeHTTP(w, r.WithContext(ctx))
	})
}
