package app

import (
	"errors"
	"fmt"
	"net/http"
)

// accountPolicy is the password policy for local user accounts. A password is
// always required; existing accounts are only checked at their next change.
type accountPolicy struct {
	MinLength                   int
	Upper, Lower, Digit, Symbol bool
}

var errAccountPolicyConfig = errors.New("account password policy is misconfigured")

// strictestAccountPolicy is what is advertised (and enforced) on corrupt config.
var strictestAccountPolicy = accountPolicy{MinLength: maxSharePasswordBytes, Upper: true, Lower: true, Digit: true, Symbol: true}

// validateAccountSetting checks the account policy keys. owned is false for keys it does not own.
func validateAccountSetting(key, val string) (owned bool, err error) {
	switch key {
	case "account_password_require_upper", "account_password_require_lower",
		"account_password_require_digit", "account_password_require_symbol":
		if _, ok := parseStrictBool(val); !ok {
			return true, fmt.Errorf("%s must be true or false", key)
		}
		return true, nil
	case "account_password_min_length":
		if _, ok := parseStrictInt(val, 1, maxSharePasswordBytes); !ok {
			return true, fmt.Errorf("account_password_min_length must be an integer from 1 to %d", maxSharePasswordBytes)
		}
		return true, nil
	}
	return false, nil
}

// loadAccountPolicy reads the stored policy and fails closed on corrupt values.
func (s *Server) loadAccountPolicy() (accountPolicy, error) {
	var p accountPolicy
	flags := []struct {
		key string
		dst *bool
	}{
		{"account_password_require_upper", &p.Upper},
		{"account_password_require_lower", &p.Lower},
		{"account_password_require_digit", &p.Digit},
		{"account_password_require_symbol", &p.Symbol},
	}
	for _, f := range flags {
		v, ok := parseStrictBool(s.St.Setting(f.key))
		if !ok {
			return accountPolicy{}, errAccountPolicyConfig
		}
		*f.dst = v
	}
	n, ok := parseStrictInt(s.St.Setting("account_password_min_length"), 1, maxSharePasswordBytes)
	if !ok {
		return accountPolicy{}, errAccountPolicyConfig
	}
	p.MinLength = n
	return p, nil
}

// validateAccountPassword is pure. An empty password is never valid.
func validateAccountPassword(p accountPolicy, pw string) error {
	switch checkPassword(passwordRules{MinLength: p.MinLength, Upper: p.Upper, Lower: p.Lower, Digit: p.Digit, Symbol: p.Symbol}, pw) {
	case pwTooLong:
		return fmt.Errorf("password must not exceed %d bytes", maxSharePasswordBytes)
	case pwBadUTF8:
		return errors.New("password must be valid UTF-8")
	case pwTooShort:
		return fmt.Errorf("password must be at least %d characters", p.MinLength)
	case pwNoUpper:
		return errors.New("password must contain an uppercase letter")
	case pwNoLower:
		return errors.New("password must contain a lowercase letter")
	case pwNoDigit:
		return errors.New("password must contain a digit")
	case pwNoSymbol:
		return errors.New("password must contain a symbol")
	}
	return nil
}

// requireAccountPassword writes the error response and returns false when pw
// violates the policy (400) or the stored policy is corrupt (500).
func (s *Server) requireAccountPassword(w http.ResponseWriter, pw string) bool {
	p, err := s.loadAccountPolicy()
	if err != nil {
		jerr(w, 500, "password policy unavailable")
		return false
	}
	if err := validateAccountPassword(p, pw); err != nil {
		jerr(w, 400, err.Error())
		return false
	}
	return true
}
