// Package account 用户身份。当前产品决策（2026-09）：用户名即唯一身份、
// 不落库；PassHash 为将来注册/密码功能预留（空串=无密码）。
package account

import "errors"

// MaxNameRunes 用户名长度上限（按字符数计）
const MaxNameRunes = 10

// 名字校验失败原因（文案与前端展示一致，由 hub 直接映射为 LOGIN_FAIL.msg）
var (
	ErrNameEmpty   = errors.New("用户名不能为空")
	ErrNameTooLong = errors.New("名字不超过10个字")
)

// ValidateName 校验用户名合法性。空名与超长分别返回 ErrNameEmpty/ErrNameTooLong；
// 重名（在线会话维度）依赖运行时会话表，不在本包校验。
func ValidateName(name string) error {
	if name == "" {
		return ErrNameEmpty
	}
	if len([]rune(name)) > MaxNameRunes {
		return ErrNameTooLong
	}
	return nil
}

// Person 登录用户
type Person struct {
	UID      int64  // 会话内自增分配（当前无持久化身份）
	Name     string // 登录名（唯一身份）
	PassHash string // 预留：空串=无密码
}

// New 创建登录用户；名字合法性由调用方先经 ValidateName 校验
func New(uid int64, name string) *Person {
	return &Person{UID: uid, Name: name}
}
