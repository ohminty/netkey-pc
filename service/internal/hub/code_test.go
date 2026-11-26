package hub

import "testing"

// Vectors from the box's netkey/hubclient.py, so both sides agree on codes and key checks.
func TestKeyFingerprintMatchesTheBox(t *testing.T) {
	cases := []struct{ pub, fp, words string }{
		{"AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8=", "CC6WTAB6RGSPD48JAH4BQCJV", "CC6W-TAB6-RGSP"},
		{"//////////////////////////////////////////8=", "NYB16XGFE9HNZFDM99D0MRY3", "NYB1-6XGF-E9HN"},
		{"ZGVmZ2hpamtsbW5vcHFyc3R1dnd4eXp7fH1+f4CBgoM=", "WFGPF05YSXG1TNF4EMF7TWHE", "WFGP-F05Y-SXG1"},
	}
	for _, c := range cases {
		fp, err := KeyFingerprint(c.pub, fpChars)
		if err != nil || fp != c.fp {
			t.Errorf("KeyFingerprint(%s) = %q, %v; want %q", c.pub, fp, err, c.fp)
		}
		if w := VerifyWords(c.pub); w != c.words {
			t.Errorf("VerifyWords(%s) = %q; want %q", c.pub, w, c.words)
		}
	}
	if _, err := KeyFingerprint("short", fpChars); err == nil {
		t.Error("a bad key must be refused")
	}
}

func TestParseCode(t *testing.T) {
	id, fp, err := ParseCode("key-0o1l-abcd-2222-3333-4444-5555-6666-7777")
	if err != nil || id != "0011ABCD" || fp != "222233334444555566667777" {
		t.Fatalf("got %q %q %v", id, fp, err)
	}
	if !IsCode(" key-0011 ") || IsCode("netkey1-abc") {
		t.Error("IsCode")
	}
	for _, bad := range []string{"KEY-0011", "KEY-0011-ABCD-2222-3333-4444-5555-6666-777U", "netkey1-aaaa"} {
		if _, _, err := ParseCode(bad); err == nil {
			t.Errorf("%q must be refused", bad)
		}
	}
}
