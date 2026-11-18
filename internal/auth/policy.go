package auth

import (
	"strings"
	"unicode/utf8"
)

// Password policy (NIST SP 800-63B style): a minimum length, no composition rules, but refuse
// passwords that are common, made of one repeated character, or contain the account's e-mail name.

var common = map[string]bool{}

func init() {
	for _, p := range strings.Fields(`password password1 password12 password123 password1234 123456789 1234567890
		12345678910 qwertyuiop qwerty1234 qwerty12345 1q2w3e4r5t 1qaz2wsx3edc iloveyou123 abcdefghij abc1234567
		0123456789 9876543210 1111111111 0000000000 letmein123 welcome123 admin12345 administrator
		passw0rd123 changeme123 lebanon123 beirut1234 paxve12345 football123 sunshine123 princess123
		azertyuiop motdepasse superman123 dragon1234 master1234 trustno1234`) {
		common[p] = true
	}
}

// CheckPassword returns "" when pw is acceptable, else an error code for the web app:
// "weak_password" (too short) or "common_password".
func CheckPassword(pw, email string, minLen int) string {
	if minLen < MinLength {
		minLen = MinLength
	}
	if utf8.RuneCountInString(pw) < minLen || len(pw) > 1024 {
		return "weak_password"
	}
	low := strings.ToLower(pw)
	if common[low] {
		return "common_password"
	}
	first, _ := utf8.DecodeRuneInString(low)
	if strings.Trim(low, string(first)) == "" {
		return "common_password"
	}
	if name, _, ok := strings.Cut(strings.ToLower(email), "@"); ok && utf8.RuneCountInString(name) >= 4 && strings.Contains(low, name) {
		return "common_password"
	}
	return ""
}
