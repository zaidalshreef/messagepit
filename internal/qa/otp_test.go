package qa

import (
	"errors"
	"testing"
)

func TestVerificationCode(t *testing.T) {
	for _, tc := range []struct {
		text, want string
		err        error
	}{
		{"Your verification code is 123456.", "123456", nil},
		{"Your OTP: 0123; OTP: 0123", "0123", nil},
		{"رمز التحقق هو 987654", "987654", nil},
		{"Order 123456 shipped", "", ErrNoCode},
		{"Verification code 123456 and OTP 987654", "", ErrAmbiguousCode},
		{"Your OTP is 12345678901", "", ErrNoCode},
	} {
		got, err := VerificationCode(tc.text)
		if got != tc.want || !errors.Is(err, tc.err) {
			t.Errorf("%q: got %q, %v; want %q, %v", tc.text, got, err, tc.want, tc.err)
		}
	}
}
