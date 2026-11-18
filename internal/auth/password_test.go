package auth

import "testing"

func TestHashAndVerify(t *testing.T) {
	h, err := Hash("correct horse battery")
	if err != nil {
		t.Fatal(err)
	}
	if !Verify("correct horse battery", h) {
		t.Fatal("correct password rejected")
	}
	if Verify("wrong password", h) {
		t.Fatal("wrong password accepted")
	}
	if Verify("x", "not-a-hash") || Verify("x", "$argon2id$v=19$m=1,t=1,p=1$$") {
		t.Fatal("malformed hash accepted")
	}
	h2, _ := Hash("correct horse battery")
	if h == h2 {
		t.Fatal("hashes are not salted")
	}
}

func TestRandomPassword(t *testing.T) {
	a, b := RandomPassword(), RandomPassword()
	if len(a) < 16 || a == b {
		t.Fatalf("weak random passwords: %q %q", a, b)
	}
}
