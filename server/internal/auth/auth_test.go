package auth

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/pquerna/otp/totp"
)

func TestInitData(t *testing.T) {
	const token = "123:ABC"
	now := time.Unix(1_800_000_000, 0)
	fields := map[string]string{
		"auth_date": fmt.Sprint(now.Add(-10 * time.Minute).Unix()),
		"query_id":  "AAH",
		"user":      `{"id":42,"first_name":"A","username":"a"}`,
	}
	data := SignInitData(token, fields)
	u, err := VerifyInitData(data, token, time.Hour, now)
	if err != nil || u.ID != 42 {
		t.Fatalf("valid data rejected: %v %+v", err, u)
	}
	if _, err := VerifyInitData(data, "123:OTHER", time.Hour, now); err == nil {
		t.Fatal("wrong bot token accepted")
	}
	if _, err := VerifyInitData(data, token, time.Hour, now.Add(time.Hour)); err == nil {
		t.Fatal("stale auth_date accepted")
	}
	tampered := strings.Replace(data, "42", "43", 1)
	if _, err := VerifyInitData(tampered, token, time.Hour, now); err == nil {
		t.Fatal("tampered user accepted")
	}
	if _, err := VerifyInitData("auth_date=1", token, time.Hour, now); err == nil {
		t.Fatal("missing hash accepted")
	}
}

// Fixture computed independently with Python's hmac module.
func TestInitDataKnownVector(t *testing.T) {
	data := "auth_date=1800000000&query_id=Q&user=%7B%22id%22%3A7%7D&hash=" + knownHash
	if _, err := VerifyInitData(data, "1:T", time.Hour, time.Unix(1800000000, 0)); err != nil {
		t.Fatalf("known vector rejected: %v", err)
	}
}

func TestCheckLogin(t *testing.T) {
	hash, err := HashPassword("s3cret")
	if err != nil {
		t.Fatal(err)
	}
	secret, uri, err := NewTOTP("owner")
	if err != nil || !strings.HasPrefix(uri, "otpauth://totp/") {
		t.Fatalf("%v %s", err, uri)
	}
	now := time.Now()
	m := &Manager{PasswordHash: hash, TOTPSecret: secret, Now: func() time.Time { return now }}
	code, _ := totp.GenerateCode(secret, now)
	if err := m.CheckLogin("s3cret", code); err != nil {
		t.Fatalf("valid login rejected: %v", err)
	}
	if m.CheckLogin("wrong", code) == nil || m.CheckLogin("s3cret", "000000") == nil && code != "000000" {
		t.Fatal("bad credentials accepted")
	}
}

func TestCookieSigning(t *testing.T) {
	m := &Manager{SessionSecret: []byte("k")}
	v := m.sign("abc")
	if id, ok := m.verify(v); !ok || id != "abc" {
		t.Fatal("signed cookie rejected")
	}
	if _, ok := m.verify("abc.forged"); ok {
		t.Fatal("forged cookie accepted")
	}
	if _, ok := (&Manager{SessionSecret: []byte("other")}).verify(v); ok {
		t.Fatal("cookie from another secret accepted")
	}
}

func TestFailLimiter(t *testing.T) {
	now := time.Unix(0, 0)
	l := NewFailLimiter(5, 15*time.Minute)
	l.now = func() time.Time { return now }
	for i := 0; i < 5; i++ {
		if l.Blocked("ip") {
			t.Fatalf("blocked after %d", i)
		}
		l.Fail("ip")
	}
	if !l.Blocked("ip") || l.Blocked("other") {
		t.Fatal("limit not applied per key")
	}
	now = now.Add(16 * time.Minute)
	if l.Blocked("ip") {
		t.Fatal("window did not expire")
	}
}

const knownHash = "f40685b907b704614da5e7875b365289d3ce75f9f94aa66ee808b0d9dba48ed0"

func TestTickets(t *testing.T) {
	now := time.Unix(0, 0)
	tk := NewTickets()
	tk.now = func() time.Time { return now }
	v, err := tk.Issue("sess")
	if err != nil {
		t.Fatal(err)
	}
	if id, ok := tk.Redeem(v); !ok || id != "sess" {
		t.Fatal("fresh ticket rejected")
	}
	if _, ok := tk.Redeem(v); ok {
		t.Fatal("ticket redeemed twice")
	}
	v, _ = tk.Issue("sess")
	now = now.Add(TicketTTL + time.Second)
	if _, ok := tk.Redeem(v); ok {
		t.Fatal("expired ticket accepted")
	}
	if _, ok := tk.Redeem("unknown"); ok {
		t.Fatal("unknown ticket accepted")
	}
}
