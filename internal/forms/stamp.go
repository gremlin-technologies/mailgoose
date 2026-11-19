package forms

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"strconv"
	"strings"
	"time"
)

// ErrBadStamp means a form timestamp that this form didn't sign.
var ErrBadStamp = errors.New("the form's timestamp isn't valid")

// Stamp signs a time for a form: "<unix seconds>.<signature>". It goes in
// the embed code's hidden _mg_ts field, so a submission (MG-36) can tell it
// came from this form's own embed code, and when that was made.
func Stamp(key []byte, formID string, t time.Time) string {
	unix := strconv.FormatInt(t.Unix(), 10)
	return unix + "." + sign(key, formID, unix)
}

// CheckStamp returns the time a stamp signed, or ErrBadStamp when the form
// didn't sign it.
func CheckStamp(key []byte, formID, stamp string) (time.Time, error) {
	unix, sig, ok := strings.Cut(stamp, ".")
	if !ok {
		return time.Time{}, ErrBadStamp
	}
	secs, err := strconv.ParseInt(unix, 10, 64)
	if err != nil || secs < 0 {
		return time.Time{}, ErrBadStamp
	}
	if !hmac.Equal([]byte(sig), []byte(sign(key, formID, unix))) {
		return time.Time{}, ErrBadStamp
	}
	return time.Unix(secs, 0), nil
}

func sign(key []byte, formID, unix string) string {
	m := hmac.New(sha256.New, key)
	m.Write([]byte("mailgoose-form-stamp\x00" + formID + "\x00" + unix))
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}
