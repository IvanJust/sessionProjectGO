package auth

import "testing"

func TestPasswordHashSaltAndVerification(t *testing.T) {
	password := "correct horse battery staple  "
	first, err := HashPassword(password)
	if err != nil {
		t.Fatal(err)
	}
	second, err := HashPassword(password)
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatal("each password must have an independent salt")
	}
	if !CheckPassword(first, password) {
		t.Fatal("correct password rejected")
	}
	if CheckPassword(first, "correct horse battery staple") {
		t.Fatal("password whitespace was ignored")
	}
	for _, malformed := range []string{"", "pbkdf2-sha256$1$a$b", "pbkdf2-sha256$999999999$a$b", first + "$extra"} {
		if CheckPassword(malformed, password) {
			t.Fatal("malformed hash accepted")
		}
	}
}
