package main

import (
	"net/http"
	"testing"
)

func TestRepositorySiteIsolation(t *testing.T) {
	app := appForTest(t)
	if !knownSite(repositorySite) {
		t.Fatal("repository site not enabled")
	}
	for _, site := range sites {
		if site == repositorySite {
			continue
		}
		if knownSite(site) {
			t.Fatalf("%s unexpectedly enabled", site)
		}
		for _, path := range []string{"/" + site + "/login", "/api/stations/" + site + "/data", "/api/stations/" + site + "/traffic"} {
			response := request(app, http.MethodGet, path, nil)
			if response.Code != http.StatusNotFound {
				t.Errorf("%s: got %d", path, response.Code)
			}
		}
		response := request(app, http.MethodPost, "/api/auth/login", map[string]string{"email": "admin@suite.test", "password": testPassword, "site": site})
		if response.Code == http.StatusOK {
			t.Errorf("%s login unexpectedly succeeded", site)
		}
	}
	response := request(app, http.MethodPost, "/api/auth/login", map[string]string{"email": "admin@suite.test", "password": testPassword, "site": repositorySite})
	if response.Code != http.StatusOK {
		t.Fatalf("own site login: %d %s", response.Code, response.Body.String())
	}
	response = request(app, http.MethodGet, "/api/stations/"+repositorySite+"/data?view=back", nil, response.Result().Cookies()...)
	if response.Code != http.StatusOK {
		t.Fatalf("own site data: %d %s", response.Code, response.Body.String())
	}
}
