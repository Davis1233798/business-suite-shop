package main

import (
	"encoding/json"
	"net/http"
	"sync"
	"testing"
)

func TestTrafficLabConcurrencyIsolationAndConsumer(t *testing.T) {
	if repositorySite != "mes" {
		t.Skip("MES-specific traffic workload")
	}
	a := appForTest(t)
	space := token()
	admin := loginSite(t, a, "admin@suite.test", space, "mes")
	prepare := request(a, "POST", "/api/stations/mes/traffic", map[string]any{"action": "prepare", "capacity": 10}, admin...)
	if prepare.Code != 200 {
		t.Fatalf("prepare: %s", prepare.Body.String())
	}
	var prepared struct {
		Data struct {
			RunID string `json:"runId"`
		} `json:"data"`
	}
	json.Unmarshal(prepare.Body.Bytes(), &prepared)
	runID := prepared.Data.RunID
	var wg sync.WaitGroup
	errors := make(chan string, 40)
	for i := 0; i < 40; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			w := request(a, "POST", "/api/stations/mes/traffic", map[string]any{"action": "submit", "runId": runID, "key": string(rune('A' + i/2))}, admin...)
			if w.Code != 200 {
				errors <- w.Body.String()
			}
		}(i)
	}
	wg.Wait()
	close(errors)
	for e := range errors {
		t.Error(e)
	}
	snapshot := func(action string) map[string]any {
		var result struct {
			Data map[string]any `json:"data"`
		}
		var wBody []byte
		if action == "" {
			w := request(a, "GET", "/api/stations/mes/traffic?runId="+runID, nil, admin...)
			if w.Code != 200 {
				t.Fatal(w.Body.String())
			}
			wBody = w.Body.Bytes()
		} else {
			w := request(a, "POST", "/api/stations/mes/traffic", map[string]any{"action": action, "runId": runID}, admin...)
			if w.Code != 200 {
				t.Fatal(w.Body.String())
			}
			wBody = w.Body.Bytes()
		}
		json.Unmarshal(wBody, &result)
		return result.Data
	}
	state := snapshot("")
	if state["committed"] != float64(10) || state["remaining"] != float64(0) || state["queued"] != float64(10) {
		t.Fatalf("capacity/queue invariant: %#v", state)
	}
	key := state["records"].([]any)[0].(map[string]any)["key"].(string)
	replay := request(a, "POST", "/api/stations/mes/traffic", map[string]any{"action": "submit", "runId": runID, "key": key}, admin...)
	var replayed struct {
		Data struct {
			Outcome   string `json:"outcome"`
			Committed int    `json:"committed"`
		} `json:"data"`
	}
	json.Unmarshal(replay.Body.Bytes(), &replayed)
	if replayed.Data.Outcome != "duplicate" || replayed.Data.Committed != 10 {
		t.Fatal("retries must replay without increasing output")
	}
	state = snapshot("drain")
	if state["processed"] != float64(10) || state["queued"] != float64(0) {
		t.Fatalf("consumer: %#v", state)
	}
	state = snapshot("drain")
	if state["processed"] != float64(10) {
		t.Fatal("consumer must be idempotent")
	}
	other := loginSite(t, a, "admin@suite.test", token(), "mes")
	if w := request(a, "GET", "/api/stations/mes/traffic?runId="+runID, nil, other...); w.Code != 404 {
		t.Fatal("cross-tenant run leak")
	}
	readonly := loginSite(t, a, "viewer@suite.test", space, "mes")
	if w := request(a, "POST", "/api/stations/mes/traffic", map[string]any{"action": "prepare", "capacity": 10}, readonly...); w.Code != http.StatusForbidden {
		t.Fatal("readonly must not write")
	}
}
