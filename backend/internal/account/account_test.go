package account

import "testing"

func TestValidateName(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want error
	}{
		{"空名", "", ErrNameEmpty},
		{"10 个汉字", "一二三四五六七八九十", nil},
		{"11 个汉字", "一二三四五六七八九十一", ErrNameTooLong},
		{"ascii 11 字符", "abcdefghijk", ErrNameTooLong},
		{"正常名", "玩家甲", nil},
	}
	for _, tc := range cases {
		if got := ValidateName(tc.in); got != tc.want {
			t.Errorf("%s: ValidateName(%q) = %v, 期望 %v", tc.name, tc.in, got, tc.want)
		}
	}
}
