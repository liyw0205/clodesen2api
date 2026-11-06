package server

import (
	"context"
	"encoding/json"
	"net/http"

	"clodesen2api/internal/apikey"
	"clodesen2api/internal/modelpolicy"
	"clodesen2api/internal/upstream"
)

func forwardPublicModels(ctx context.Context, w http.ResponseWriter, up *upstream.Upstream, policies *modelpolicy.Store, auth *apikey.Authenticator, principal apikey.Principal) (error, bool) {
	raw, err := up.ListModels(ctx)
	if err != nil {
		return err, false
	}
	ids, err := modelpolicy.IDs(raw)
	if err != nil {
		return err, false
	}
	if err := policies.Register(ctx, ids); err != nil {
		return err, false
	}
	snapshot, err := policies.Snapshot(ctx)
	if err != nil {
		return err, false
	}
	var payload struct {
		Data []map[string]json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return err, false
	}
	available := make(map[string]map[string]json.RawMessage)
	for _, row := range payload.Data {
		var id string
		_ = json.Unmarshal(row["id"], &id)
		available[id] = row
	}
	data := make([]map[string]json.RawMessage, 0, len(snapshot.Models))
	for _, rule := range snapshot.Models {
		resolved, err := snapshot.Route(rule)
		if err != nil {
			continue
		}
		row, ok := available[resolved.UpstreamID]
		if !ok || !snapshot.Available(rule) {
			continue
		}
		if auth != nil {
			if denied := auth.CheckModel(principal, rule.SourceID); denied != nil {
				if denied.Status != 403 {
					writeAuthError(w, denied)
					return denied, true
				}
				continue
			}
		}
		add := func(id string) {
			item := make(map[string]json.RawMessage, len(row))
			for key, value := range row {
				item[key] = value
			}
			item["id"], _ = json.Marshal(id)
			data = append(data, item)
		}
		add(rule.PublicID)
		if rule.KeepOriginal && rule.SourceID != rule.PublicID {
			add(rule.SourceID)
		}
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	err = json.NewEncoder(w).Encode(map[string]any{"object": "list", "data": data})
	return err, true
}
