package nodes

import (
	"bytes"
	"context"
	"crypto/rand"
	_ "embed"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"sync"
	"text/template"
	"time"

	"github.com/andrewsl105/fleetpocket/server/internal/hosts"
	"github.com/andrewsl105/fleetpocket/server/internal/store"
)

// Pairing connects a new computer without copying a token: the node asks for
// a short code, the owner approves the code in the app (one click when the app
// runs on that computer), and the node then collects its token once.
// Like a TV sign-in (device authorization grant), kept in memory: a code
// lives PairingTTL and works once.

const (
	PairingTTL     = 10 * time.Minute
	maxPending     = 20
	maxPerIP       = 5
	codeAlphabet   = "23456789ABCDEFGHJKMNPQRSTUVWXYZ"
	codeLen        = 6
	pairingPollKey = 32
)

var (
	ErrPairingNotFound = errors.New("no such pairing code, or it expired")
	ErrTooManyPairings = errors.New("too many pairing requests, try again in a few minutes")
)

// PairingRequest is what fleetpocket-node sends to start pairing.
type PairingRequest struct {
	Hostname string   `json:"hostname"`
	Platform string   `json:"platform"`
	Agents   []string `json:"agents"`
}

// PairingStart is the node's answer: the code to show and the secret to poll with.
type PairingStart struct {
	Code      string `json:"code"`
	PollID    string `json:"poll_id"`
	ExpiresIn int    `json:"expires_in"`
	// ApproveURL opens the app where the owner enters or confirms the code.
	ApproveURL string `json:"approve_url"`
}

// PairingResult is returned to the node once, after approval.
type PairingResult struct {
	Status string `json:"status"` // pending | approved
	NodeID string `json:"node_id,omitempty"`
	Name   string `json:"name,omitempty"`
	Token  string `json:"token,omitempty"`
	// RunnerImage is the server's agent image, so setup can pull it.
	RunnerImage string `json:"runner_image,omitempty"`
}

type pairing struct {
	PairingRequest
	code    string
	pollID  string
	ip      string
	expires time.Time
	result  *PairingResult
}

type pairings struct {
	mu   sync.Mutex
	byID map[string]*pairing
	now  func() time.Time
}

func (p *pairings) init() {
	if p.byID == nil {
		p.byID = map[string]*pairing{}
	}
	if p.now == nil {
		p.now = time.Now
	}
}

// pruneLocked drops expired requests.
func (p *pairings) pruneLocked() {
	now := p.now()
	for id, x := range p.byID {
		if now.After(x.expires) {
			delete(p.byID, id)
		}
	}
}

func randomCode() string {
	b := make([]byte, codeLen)
	_, _ = rand.Read(b)
	out := make([]byte, 0, codeLen+1)
	for i, v := range b {
		if i == codeLen/2 {
			out = append(out, '-')
		}
		out = append(out, codeAlphabet[int(v)%len(codeAlphabet)])
	}
	return string(out)
}

// NormalizeCode accepts codes typed with spaces, lowercase or without the dash.
func NormalizeCode(code string) string {
	c := strings.ToUpper(strings.NewReplacer("-", "", " ", "").Replace(code))
	if len(c) != codeLen {
		return c
	}
	return c[:codeLen/2] + "-" + c[codeLen/2:]
}

// StartPairing registers a pairing request from a node (unauthenticated,
// limited per IP and in total).
func (s *Service) StartPairing(in PairingRequest, ip string) (*PairingStart, error) {
	p := &s.pairing
	p.mu.Lock()
	defer p.mu.Unlock()
	p.init()
	p.pruneLocked()
	perIP := 0
	for _, x := range p.byID {
		if x.ip == ip {
			perIP++
		}
	}
	if len(p.byID) >= maxPending || perIP >= maxPerIP {
		return nil, ErrTooManyPairings
	}
	in.Hostname = clip(strings.TrimSpace(in.Hostname), 64)
	in.Platform = clip(strings.TrimSpace(in.Platform), 32)
	if len(in.Agents) > 8 {
		in.Agents = in.Agents[:8]
	}
	poll := make([]byte, pairingPollKey)
	_, _ = rand.Read(poll)
	x := &pairing{PairingRequest: in, pollID: base64.RawURLEncoding.EncodeToString(poll), ip: ip, expires: p.now().Add(PairingTTL)}
	for {
		x.code = randomCode()
		if !p.codeTakenLocked(x.code) {
			break
		}
	}
	p.byID[x.pollID] = x
	return &PairingStart{Code: x.code, PollID: x.pollID, ExpiresIn: int(PairingTTL.Seconds())}, nil
}

func (p *pairings) codeTakenLocked(code string) bool {
	for _, x := range p.byID {
		if x.code == code {
			return true
		}
	}
	return false
}

// PollPairing is the node waiting for approval. An approved result is handed
// out once and then forgotten (the token exists only in that response).
func (s *Service) PollPairing(pollID string) (*PairingResult, error) {
	p := &s.pairing
	p.mu.Lock()
	defer p.mu.Unlock()
	p.init()
	p.pruneLocked()
	x, ok := p.byID[pollID]
	if !ok {
		return nil, ErrPairingNotFound
	}
	if x.result == nil {
		return &PairingResult{Status: "pending"}, nil
	}
	delete(p.byID, pollID)
	return x.result, nil
}

// PendingPairing is what the owner sees before approving a code.
type PendingPairing struct {
	Code      string    `json:"code"`
	Hostname  string    `json:"hostname"`
	Platform  string    `json:"platform"`
	Agents    []string  `json:"agents"`
	IP        string    `json:"ip"`
	ExpiresAt time.Time `json:"expires_at"`
}

// LookupPairing (owner session) describes a waiting computer by its code, so
// the app can show what is being connected before the owner approves.
func (s *Service) LookupPairing(code string) (*PendingPairing, error) {
	code = NormalizeCode(code)
	p := &s.pairing
	p.mu.Lock()
	defer p.mu.Unlock()
	p.init()
	p.pruneLocked()
	for _, x := range p.byID {
		if x.code == code && x.result == nil {
			return &PendingPairing{Code: x.code, Hostname: x.Hostname, Platform: x.Platform, Agents: nonNil(x.Agents), IP: x.ip, ExpiresAt: x.expires}, nil
		}
	}
	return nil, ErrPairingNotFound
}

// ApprovePairing (owner session) registers a node for the code and hands its
// token to the waiting node. name defaults to the computer's host name.
func (s *Service) ApprovePairing(ctx context.Context, code, name string) (*store.NodeView, error) {
	code = NormalizeCode(code)
	p := &s.pairing
	p.mu.Lock()
	p.init()
	p.pruneLocked()
	var x *pairing
	for _, c := range p.byID {
		if c.code == code && c.result == nil {
			x = c
		}
	}
	p.mu.Unlock()
	if x == nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalid, ErrPairingNotFound)
	}
	name = strings.TrimSpace(name)
	if name == "" {
		name = x.Hostname
	}
	if name == "" {
		name = "Computer"
	}
	id, err := s.freeID(ctx, slug(name))
	if err != nil {
		return nil, err
	}
	tok, hash := NewToken(id)
	n, err := s.Store.CreateNode(ctx, id, name, hash)
	if err != nil {
		return nil, err
	}
	// The agents it reports in the pairing request show until its first hello.
	if len(x.Agents) > 0 || x.Platform != "" {
		if reported, err := s.Store.SetNodeReport(ctx, id, nonNil(x.Agents), x.Platform); err == nil {
			n = reported
		}
	}
	p.mu.Lock()
	x.result = &PairingResult{Status: "approved", NodeID: id, Name: name, Token: tok, RunnerImage: s.RunnerImage}
	p.mu.Unlock()
	v := s.View(ctx, n)
	s.Hub.PublishNode(v)
	return v, nil
}

// freeID returns base, or base-2, base-3, … when taken.
func (s *Service) freeID(ctx context.Context, base string) (string, error) {
	if base == "" || base == hosts.ServerID {
		base = "computer"
	}
	for i := 1; i < 100; i++ {
		id := base
		if i > 1 {
			suffix := fmt.Sprintf("-%d", i)
			id = clip(base, 32-len(suffix)) + suffix
		}
		if _, err := s.Store.GetNode(ctx, id); errors.Is(err, store.ErrNotFound) {
			return id, nil
		} else if err != nil {
			return "", err
		}
	}
	return "", errors.New("no free computer id")
}

// slug turns a host name like "Andrii's MacBook-Pro.local" into "andriis-macbook-pro".
func slug(s string) string {
	s = strings.TrimSuffix(strings.ToLower(s), ".local")
	var b strings.Builder
	dash := false
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			dash = false
		case r == '\'' || r == '’':
		default:
			if b.Len() > 0 && !dash {
				b.WriteByte('-')
				dash = true
			}
		}
	}
	out := strings.Trim(b.String(), "-")
	out = strings.Trim(clip(out, 32), "-")
	if !idRe.MatchString(out) {
		return ""
	}
	return out
}

func clip(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// ---- install script ----

// DefaultDownloadURL is where release builds of fleetpocket-node are published.
const DefaultDownloadURL = "https://github.com/AndrewSl105/fleetpocket/releases/download/node-latest"

//go:embed install.sh
var installScript string

var installTmpl = template.Must(template.New("install").Parse(installScript))

// InstallScript renders the install script for this server. server and
// download must be https URLs without quotes (they are checked).
func InstallScript(server, download string) ([]byte, error) {
	for _, u := range []string{server, download} {
		if !strings.HasPrefix(u, "https://") && !strings.HasPrefix(u, "http://") || strings.ContainsAny(u, "'\"\\\n\r` $") {
			return nil, fmt.Errorf("unsafe URL %q in the install script", u)
		}
	}
	var b bytes.Buffer
	err := installTmpl.Execute(&b, map[string]string{"Server": server, "Download": download})
	return b.Bytes(), err
}
