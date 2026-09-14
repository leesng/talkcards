// Package account: user identity. Product decision (2026-09): the username is
// the sole identity and is not persisted; PassHash is reserved for a future
// registration/password feature ("" = no password).
package account

import "errors"

// MaxNameRunes is the username length limit, counted in runes.
const MaxNameRunes = 10

// Messages map directly to the LOGIN_FAIL.msg shown by the hub.
var (
	ErrNameEmpty   = errors.New("用户名不能为空")
	ErrNameTooLong = errors.New("名字不超过10个字")
)

// ValidateName checks username validity. Duplicate-name rejection is based on
// the live session registry and is therefore not checked here.
func ValidateName(name string) error {
	if name == "" {
		return ErrNameEmpty
	}
	if len([]rune(name)) > MaxNameRunes {
		return ErrNameTooLong
	}
	return nil
}

type Person struct {
	UID      int64  // assigned per-session (no persistent identity yet)
	Name     string // login name (sole identity)
	PassHash string // reserved: "" = no password
}

func New(uid int64, name string) *Person {
	return &Person{UID: uid, Name: name}
}
