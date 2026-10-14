package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

var ErrInitData = errors.New("invalid Telegram init data")

type TelegramUser struct {
	ID        int64  `json:"id"`
	FirstName string `json:"first_name"`
	Username  string `json:"username"`
}

// VerifyInitData checks Mini App initData per Telegram's documented
// algorithm: secret = HMAC_SHA256("WebAppData", bot_token);
// hash = hex(HMAC_SHA256(secret, data_check_string)), where data_check_string
// is every field except `hash`, sorted, as key=value joined by "\n".
func VerifyInitData(initData, botToken string, maxAge time.Duration, now time.Time) (*TelegramUser, error) {
	vals, err := url.ParseQuery(initData)
	if err != nil {
		return nil, ErrInitData
	}
	got := vals.Get("hash")
	if got == "" {
		return nil, ErrInitData
	}
	keys := make([]string, 0, len(vals))
	for k := range vals {
		if k != "hash" {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	lines := make([]string, len(keys))
	for i, k := range keys {
		lines[i] = k + "=" + vals.Get(k)
	}
	secret := hmacSHA256([]byte("WebAppData"), []byte(botToken))
	want := hex.EncodeToString(hmacSHA256(secret, []byte(strings.Join(lines, "\n"))))
	if !hmac.Equal([]byte(want), []byte(strings.ToLower(got))) {
		return nil, ErrInitData
	}
	authDate, err := strconv.ParseInt(vals.Get("auth_date"), 10, 64)
	if err != nil || now.Sub(time.Unix(authDate, 0)) > maxAge {
		return nil, ErrInitData
	}
	var u TelegramUser
	if err := json.Unmarshal([]byte(vals.Get("user")), &u); err != nil || u.ID == 0 {
		return nil, ErrInitData
	}
	return &u, nil
}

func hmacSHA256(key, data []byte) []byte {
	m := hmac.New(sha256.New, key)
	m.Write(data)
	return m.Sum(nil)
}

// SignInitData builds valid initData for tests and local development.
func SignInitData(botToken string, fields map[string]string) string {
	keys := make([]string, 0, len(fields))
	for k := range fields {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	lines := make([]string, len(keys))
	v := url.Values{}
	for i, k := range keys {
		lines[i] = k + "=" + fields[k]
		v.Set(k, fields[k])
	}
	secret := hmacSHA256([]byte("WebAppData"), []byte(botToken))
	v.Set("hash", hex.EncodeToString(hmacSHA256(secret, []byte(strings.Join(lines, "\n")))))
	return v.Encode()
}
