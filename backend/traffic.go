package main

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"time"
)

// These dedicated tables exercise real SQLite transactions without changing operational records.
func (a *App) trafficAPI(w http.ResponseWriter, r *http.Request) {
	started := time.Now()
	site := r.PathValue("site")
	if site != repositorySite {
		fail(w, 404, "Traffic lab is unavailable for this workspace")
		return
	}
	id, err := a.identity(r)
	if err != nil {
		fail(w, 401, "Sign in to use the traffic lab")
		return
	}
	grant := id.grant(site)
	if grant == nil || grant.Scope != "back" {
		fail(w, 403, "Back-office access is required")
		return
	}
	w.Header().Set("Cache-Control", "private, no-store")
	runID := r.URL.Query().Get("runId")
	input := struct {
		Action   string `json:"action"`
		RunID    string `json:"runId"`
		Key      string `json:"key"`
		Capacity int    `json:"capacity"`
	}{}
	if r.Method == "POST" {
		if grant.Role == "readonly" {
			fail(w, 403, "A write-enabled role is required")
			return
		}
		if json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&input) != nil {
			fail(w, 400, "Invalid lab request")
			return
		}
		runID = input.RunID
	} else if r.Method != "GET" {
		fail(w, 405, "Use GET or POST")
		return
	}
	if input.Action == "prepare" {
		if input.Capacity < 1 || input.Capacity > 1000 {
			fail(w, 400, "Capacity must be between 1 and 1000")
			return
		}
		// Retain a bounded history for each organization and station.
		tx, e := a.db.BeginTx(r.Context(), nil)
		if e != nil {
			fail(w, 503, "Database unavailable")
			return
		}
		defer tx.Rollback()
		_, e = tx.Exec("DELETE FROM traffic_runs WHERE organization_id=? AND site=? AND id NOT IN (SELECT id FROM traffic_runs WHERE organization_id=? AND site=? ORDER BY created_at DESC,rowid DESC LIMIT 9)", id.OrganizationID, site, id.OrganizationID, site)
		runID = token()
		if e == nil {
			_, e = tx.Exec("INSERT INTO traffic_runs(id,organization_id,site,capacity) VALUES(?,?,?,?)", runID, id.OrganizationID, site, input.Capacity)
		}
		if e != nil || tx.Commit() != nil {
			fail(w, 503, "Could not prepare the lab")
			return
		}
		ok(w, map[string]any{"runId": runID, "capacity": input.Capacity})
		return
	}
	if runID == "" {
		fail(w, 400, "A run ID is required")
		return
	}
	tx, e := a.db.BeginTx(r.Context(), nil)
	if e != nil {
		fail(w, 503, "Database unavailable")
		return
	}
	defer tx.Rollback()
	var capacity, committed int
	e = tx.QueryRow("SELECT capacity,committed FROM traffic_runs WHERE id=? AND organization_id=? AND site=?", runID, id.OrganizationID, site).Scan(&capacity, &committed)
	if e == sql.ErrNoRows {
		fail(w, 404, "Lab run not found in this workspace")
		return
	}
	if e != nil {
		fail(w, 503, "Could not read lab run")
		return
	}
	outcome := ""
	sequence := 0
	switch input.Action {
	case "submit":
		if len(input.Key) < 1 || len(input.Key) > 128 {
			fail(w, 400, "Request key must contain 1 to 128 characters")
			return
		}
		e = tx.QueryRow("SELECT sequence FROM traffic_records WHERE run_id=? AND request_key=?", runID, input.Key).Scan(&sequence)
		if e == nil {
			outcome = "duplicate"
		} else if e != sql.ErrNoRows {
			fail(w, 503, "Could not read request key")
			return
		} else if committed >= capacity {
			outcome = "capacity_rejected"
		} else {
			result, err := tx.Exec("UPDATE traffic_runs SET committed=committed+1 WHERE id=? AND committed<capacity", runID)
			if err != nil {
				fail(w, 503, "Commit failed")
				return
			}
			n, err := result.RowsAffected()
			if err != nil || n != 1 {
				fail(w, 503, "Capacity guard failed")
				return
			}
			committed++
			sequence = committed
			if _, e = tx.Exec("INSERT INTO traffic_records(run_id,request_key,sequence) VALUES(?,?,?)", runID, input.Key, sequence); e != nil {
				fail(w, 503, "Record write failed")
				return
			}
			outcome = "accepted"
		}
	case "drain":
		if _, e = tx.Exec("UPDATE traffic_records SET processed=1 WHERE run_id=? AND processed=0", runID); e != nil {
			fail(w, 503, "Consumer failed")
			return
		}
	case "":
		if r.Method != "GET" {
			fail(w, 400, "Unknown lab action")
			return
		}
	default:
		fail(w, 400, "Unknown lab action")
		return
	}
	var queued, processed int
	if e = tx.QueryRow("SELECT count(*) FILTER (WHERE processed=0),count(*) FILTER (WHERE processed=1) FROM traffic_records WHERE run_id=?", runID).Scan(&queued, &processed); e != nil {
		fail(w, 503, "Queue read failed")
		return
	}
	records := []map[string]any{}
	if r.Method == "GET" || input.Action == "drain" {
		rows, err := tx.Query("SELECT request_key,sequence,created_at FROM traffic_records WHERE run_id=? ORDER BY sequence DESC LIMIT 50", runID)
		if err != nil {
			fail(w, 503, "Audit read failed")
			return
		}
		for rows.Next() {
			var key, created string
			var seq int
			if rows.Scan(&key, &seq, &created) != nil {
				rows.Close()
				fail(w, 503, "Audit read failed")
				return
			}
			records = append(records, map[string]any{"key": key, "sequence": seq, "createdAt": created})
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			fail(w, 503, "Audit read failed")
			return
		}
	}
	if tx.Commit() != nil {
		fail(w, 503, "Transaction failed")
		return
	}
	ok(w, map[string]any{"runId": runID, "outcome": outcome, "capacity": capacity, "committed": committed, "remaining": capacity - committed, "sequence": sequence, "queued": queued, "processed": processed, "records": records, "elapsedMs": float64(time.Since(started).Microseconds()) / 1000})
}
