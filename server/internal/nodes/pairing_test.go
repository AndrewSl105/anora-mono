package nodes

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/andrewsl105/fleetpocket/server/internal/events"
	"github.com/andrewsl105/fleetpocket/server/internal/hosts"
	"github.com/andrewsl105/fleetpocket/server/internal/testutil"
)

func TestSlugAndCode(t *testing.T) {
	for in, want := range map[string]string{
		"Andrii's MacBook-Pro.local": "andriis-macbook-pro",
		"  Studio  Mac ":             "studio-mac",
		"服务器":                        "",
		"server":                     "server",
	} {
		if got := slug(in); got != want {
			t.Errorf("slug(%q) = %q, want %q", in, got, want)
		}
	}
	if c := randomCode(); len(c) != 7 || c[3] != '-' || strings.ContainsAny(c, "01IOL") {
		t.Fatalf("code %q", c)
	}
	if NormalizeCode(" kq7 4mx ") != "KQ7-4MX" || NormalizeCode("kq74mx") != "KQ7-4MX" {
		t.Fatal("NormalizeCode")
	}
}

func TestPairing(t *testing.T) {
	st := testutil.DB(t)
	ctx := context.Background()
	s := &Service{Store: st, Hub: events.NewHub(), Registry: &hosts.Registry{}, RunnerImage: "img:latest"}

	start, err := s.StartPairing(PairingRequest{Hostname: "Andrii's MacBook-Pro.local", Platform: "darwin/arm64", Agents: []string{"claude"}}, "1.2.3.4")
	if err != nil {
		t.Fatal(err)
	}
	if res, err := s.PollPairing(start.PollID); err != nil || res.Status != "pending" || res.Token != "" {
		t.Fatalf("pending: %+v %v", res, err)
	}
	if pp, err := s.LookupPairing(start.Code); err != nil || pp.Hostname != "Andrii's MacBook-Pro.local" || pp.IP != "1.2.3.4" {
		t.Fatalf("lookup: %+v %v", pp, err)
	}
	if _, err := s.ApprovePairing(ctx, "AAA-AAA", ""); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unknown code: %v", err)
	}
	// Typed by hand: lowercase, no dash.
	n, err := s.ApprovePairing(ctx, strings.ToLower(strings.ReplaceAll(start.Code, "-", "")), "")
	if err != nil {
		t.Fatal(err)
	}
	if n.ID != "andriis-macbook-pro" || n.Name != "Andrii's MacBook-Pro.local" || len(n.Agents) != 1 {
		t.Fatalf("node %+v", n.Node)
	}
	if _, err := s.ApprovePairing(ctx, start.Code, ""); err == nil {
		t.Fatal("a code works twice")
	}
	res, err := s.PollPairing(start.PollID)
	if err != nil || res.Status != "approved" || res.NodeID != n.ID || res.RunnerImage != "img:latest" {
		t.Fatalf("approved: %+v %v", res, err)
	}
	// The token authenticates the node; it is handed out once.
	req, _ := http.NewRequest("GET", "/", nil)
	req.Header.Set("Authorization", "Bearer "+res.Token)
	if got, err := s.Authenticate(ctx, req); err != nil || got.ID != n.ID {
		t.Fatalf("token: %v %v", got, err)
	}
	if _, err := s.PollPairing(start.PollID); !errors.Is(err, ErrPairingNotFound) {
		t.Fatalf("second poll: %v", err)
	}

	// A second computer with the same name gets its own id.
	start2, _ := s.StartPairing(PairingRequest{Hostname: "Andrii's MacBook-Pro.local"}, "1.2.3.4")
	n2, err := s.ApprovePairing(ctx, start2.Code, "")
	if err != nil || n2.ID != "andriis-macbook-pro-2" {
		t.Fatalf("second: %+v %v", n2, err)
	}

	// Limits: per IP and in total.
	for i := 0; i < maxPerIP; i++ {
		_, _ = s.StartPairing(PairingRequest{}, "5.6.7.8")
	}
	if _, err := s.StartPairing(PairingRequest{}, "5.6.7.8"); !errors.Is(err, ErrTooManyPairings) {
		t.Fatalf("per-IP limit: %v", err)
	}
}

func TestInstallScript(t *testing.T) {
	b, err := InstallScript("https://fp.example.com", "https://github.com/o/r/releases/download/node-latest")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"SERVER='https://fp.example.com'", "fleetpocket-node-$target", "setup --server \"$SERVER\" </dev/tty"} {
		if !strings.Contains(string(b), want) {
			t.Errorf("script lacks %q", want)
		}
	}
	if _, err := InstallScript("https://x.com/'; rm -rf ~; '", "https://ok"); err == nil {
		t.Fatal("quote in URL accepted")
	}
}
