package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestPairAgainstServer(t *testing.T) {
	var polls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "POST" && r.URL.Path == "/api/v1/node/pair":
			if r.Header.Get("X-Requested-With") != "fleetpocket" {
				http.Error(w, "csrf", http.StatusForbidden)
				return
			}
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(pairStart{Code: "KQ7-4MX", PollID: "p1", ExpiresIn: 30})
		case r.URL.Path == "/api/v1/node/pair/p1":
			if polls.Add(1) < 2 {
				_ = json.NewEncoder(w).Encode(pairResult{Status: "pending"})
				return
			}
			_ = json.NewEncoder(w).Encode(pairResult{Status: "approved", NodeID: "mac", Name: "Mac", Token: "fpn_mac.x", RunnerImage: "img"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	u := &ui{in: bufio.NewReader(strings.NewReader(""))}
	res, err := pair(context.Background(), u, srv.URL, []string{"claude"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Token != "fpn_mac.x" || res.RunnerImage != "img" {
		t.Fatalf("%+v", res)
	}
}

func TestLocalPairingOnlyForServerOrigin(t *testing.T) {
	stop := serveLocalPairing("https://fp.example.com", "KQ7-4MX", "Mac")
	defer stop()
	get := func(origin string) (*http.Response, error) {
		req, _ := http.NewRequest("GET", fmt.Sprintf("http://127.0.0.1:%d/pairing", LocalPairPort), nil)
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		return http.DefaultClient.Do(req)
	}
	resp, err := get("https://evil.example")
	if err != nil {
		t.Skipf("loopback port busy: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("other origin: %d", resp.StatusCode)
	}
	resp, err = get("https://fp.example.com")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var body map[string]string
	_ = json.NewDecoder(resp.Body).Decode(&body)
	if body["code"] != "KQ7-4MX" || resp.Header.Get("Access-Control-Allow-Origin") != "https://fp.example.com" {
		t.Fatalf("own origin: %v %v", body, resp.Header)
	}
}
