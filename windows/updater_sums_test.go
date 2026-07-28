package main

import "testing"

// sumFor parses a manifest fetched over the network before anything about it has
// been verified, so it needs to be strict about what it accepts and precise about
// which entry it returns - picking up a neighbouring line's digest would mean
// rejecting a good download, or worse, accepting a bad one.
func TestSumFor(t *testing.T) {
	const manifest = `d9f8a1c0e2b3445566778899aabbccddeeff00112233445566778899aabbccdd  phantom-server-linux-amd64
1111111111111111111111111111111111111111111111111111111111111111  phantom.apk
2222222222222222222222222222222222222222222222222222222222222222  phantom.exe
3333333333333333333333333333333333333333333333333333333333333333 *phantom-keygen-linux-arm64
`

	got, err := sumFor(manifest, "phantom.exe")
	if err != nil {
		t.Fatal(err)
	}
	if want := "2222222222222222222222222222222222222222222222222222222222222222"; got != want {
		t.Fatalf("got %s, want %s", got, want)
	}

	// Binary-mode entries carry a leading '*' on the filename.
	if _, err := sumFor(manifest, "phantom-keygen-linux-arm64"); err != nil {
		t.Errorf("binary-mode entry not matched: %v", err)
	}

	// A name that isn't listed must be an error, never a silent pass.
	if _, err := sumFor(manifest, "phantom-not-here.exe"); err == nil {
		t.Error("an unlisted name was accepted")
	}
}

func TestSumForRejectsMalformed(t *testing.T) {
	for _, tc := range []struct {
		name     string
		manifest string
	}{
		{"digest too short", "abc  phantom.exe\n"},
		{"digest too long", "2222222222222222222222222222222222222222222222222222222222222222aa  phantom.exe\n"},
		{"no digest at all", "phantom.exe\n"},
		{"empty", ""},
		{"prefix of a listed name must not match", "2222222222222222222222222222222222222222222222222222222222222222  phantom.exe.sig\n"},
	} {
		if _, err := sumFor(tc.manifest, "phantom.exe"); err == nil {
			t.Errorf("%s: accepted", tc.name)
		}
	}
}

// Uppercase digests are valid sha256sum output on some platforms.
func TestSumForNormalisesCase(t *testing.T) {
	const manifest = "AABBCCDDEEFF00112233445566778899AABBCCDDEEFF00112233445566778899  phantom.exe\n"
	got, err := sumFor(manifest, "phantom.exe")
	if err != nil {
		t.Fatal(err)
	}
	if want := "aabbccddeeff00112233445566778899aabbccddeeff00112233445566778899"; got != want {
		t.Fatalf("got %s, want %s", got, want)
	}
}
