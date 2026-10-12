package adding

import "testing"

func TestParseSkip(t *testing.T) {
	got, err := ParseSkip("1-3, 40")
	if err != nil || len(got) != 4 || !got[1] || !got[3] || !got[40] || got[4] {
		t.Errorf("ParseSkip = %v, %v", got, err)
	}
	for _, bad := range []string{"x", "3-1", "0", "2-"} {
		if _, err := ParseSkip(bad); err == nil {
			t.Errorf("ParseSkip(%q) should fail", bad)
		}
	}
}
