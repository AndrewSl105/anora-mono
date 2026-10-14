// Package auth handles the single owner's login (password + TOTP), session
// cookies, and Telegram Mini App initData.
package auth

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/pquerna/otp/totp"
	"golang.org/x/crypto/bcrypt"

	"github.com/andrewsl105/anora-runner/internal/store"
)

const (
	CookieName = "anora_session"
	SessionTTL = 30 * 24 * time.Hour
)

var (
	ErrBadCredentials = errors.New("invalid password or code")
	ErrNoSession      = errors.New("no valid session")
)

type Manager struct {
	Store          *store.Store
	PasswordHash   string
	TOTPSecret     string
	SessionSecret  []byte
	CookieInsecure bool
	Now            func() time.Time
}

func (m *Manager) now() time.Time {
	if m.Now != nil {
		return m.Now()
	}
	return time.Now()
}

// CheckLogin verifies the password and the current TOTP code. Both are always
// evaluated so timing does not reveal which one failed.
func (m *Manager) CheckLogin(password, code string) error {
	pwErr := bcrypt.CompareHashAndPassword([]byte(m.PasswordHash), []byte(password))
	codeOK, _ := totp.ValidateCustom(strings.TrimSpace(code), m.TOTPSecret, m.now(), totp.ValidateOpts{
		Period: 30, Skew: 1, Digits: 6,
	})
	if pwErr != nil || !codeOK {
		return ErrBadCredentials
	}
	return nil
}

// StartSession creates a session row and sets the cookie.
func (m *Manager) StartSession(ctx context.Context, w http.ResponseWriter, kind string) error {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return err
	}
	id := base64.RawURLEncoding.EncodeToString(b)
	if err := m.Store.CreateSession(ctx, id, kind, m.now().Add(SessionTTL)); err != nil {
		return err
	}
	m.setCookie(w, m.sign(id), SessionTTL)
	return nil
}

// Session validates the cookie, slides its expiry and refreshes the cookie.
func (m *Manager) Session(ctx context.Context, w http.ResponseWriter, r *http.Request) (*store.AuthSession, error) {
	c, err := r.Cookie(CookieName)
	if err != nil {
		return nil, ErrNoSession
	}
	id, ok := m.verify(c.Value)
	if !ok {
		return nil, ErrNoSession
	}
	s, err := m.Store.TouchSession(ctx, id, SessionTTL)
	if err != nil {
		return nil, ErrNoSession
	}
	if w != nil {
		m.setCookie(w, c.Value, SessionTTL)
	}
	return s, nil
}

func (m *Manager) EndSession(ctx context.Context, w http.ResponseWriter, r *http.Request) error {
	if c, err := r.Cookie(CookieName); err == nil {
		if id, ok := m.verify(c.Value); ok {
			if err := m.Store.DeleteSession(ctx, id); err != nil {
				return err
			}
		}
	}
	m.setCookie(w, "", -1)
	return nil
}

func (m *Manager) setCookie(w http.ResponseWriter, value string, ttl time.Duration) {
	c := &http.Cookie{
		Name: CookieName, Value: value, Path: "/", HttpOnly: true,
		Secure: !m.CookieInsecure, SameSite: http.SameSiteLaxMode,
	}
	if ttl < 0 {
		c.MaxAge = -1
	} else {
		c.MaxAge = int(ttl.Seconds())
	}
	http.SetCookie(w, c)
}

// Cookie value: "<id>.<base64url HMAC-SHA256(SESSION_SECRET, id)>".
func (m *Manager) sign(id string) string {
	mac := hmac.New(sha256.New, m.SessionSecret)
	mac.Write([]byte(id))
	return id + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func (m *Manager) verify(v string) (string, bool) {
	id, _, ok := strings.Cut(v, ".")
	if !ok || id == "" {
		return "", false
	}
	return id, subtle.ConstantTimeCompare([]byte(m.sign(id)), []byte(v)) == 1
}

func HashPassword(pw string) (string, error) {
	b, err := bcrypt.GenerateFromPassword([]byte(pw), 12)
	return string(b), err
}

// NewTOTP returns a fresh base32 secret and its otpauth:// URI.
func NewTOTP(account string) (secret, uri string, err error) {
	key, err := totp.Generate(totp.GenerateOpts{Issuer: "Anora", AccountName: account})
	if err != nil {
		return "", "", err
	}
	return key.Secret(), key.URL(), nil
}
