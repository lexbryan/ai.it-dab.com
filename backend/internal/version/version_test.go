package version

import "testing"

func TestStringNonEmpty(t *testing.T) {
	if got := String(); got == "" {
		t.Fatal("version.String() must not be empty")
	}
}
