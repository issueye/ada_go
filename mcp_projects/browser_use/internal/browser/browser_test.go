package browser

import "testing"

func TestNormalizeURL(t *testing.T) {
	tests := []struct {
		in      string
		want    string
		wantErr bool
	}{
		{"", "about:blank", false},
		{"about:blank", "about:blank", false},
		{"example.com", "https://example.com", false},
		{"http://127.0.0.1:8765/form?q=1", "http://127.0.0.1:8765/form?q=1", false},
		{"https://example.com/a", "https://example.com/a", false},
		{"file:///C:/secret", "", true},
		{"javascript:alert(1)", "", true},
		{"data:text/html,hi", "", true},
	}
	for _, tt := range tests {
		got, err := NormalizeURL(tt.in)
		if tt.wantErr {
			if err == nil {
				t.Errorf("NormalizeURL(%q) = %q, want error", tt.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("NormalizeURL(%q): %v", tt.in, err)
			continue
		}
		if got != tt.want {
			t.Errorf("NormalizeURL(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestMatchURL(t *testing.T) {
	tests := []struct {
		pattern, current string
		want             bool
	}{
		{"", "https://example.com", true},
		{"/from", "https://news.ycombinator.com/from?q=x", true},
		{"/from", "https://example.com/newest", false},
		{"*/done*", "http://127.0.0.1:8765/done?q=x", true},
		{"https://example.com/*", "https://example.com/a/b", true},
	}
	for _, tt := range tests {
		if got := matchURL(tt.pattern, tt.current); got != tt.want {
			t.Errorf("matchURL(%q, %q) = %v, want %v", tt.pattern, tt.current, got, tt.want)
		}
	}
}

func TestTargetValidate(t *testing.T) {
	if err := (Target{}).Validate(); err == nil {
		t.Fatal("empty target should fail")
	}
	if err := (Target{Ref: "e1", Selector: "#x"}).Validate(); err == nil {
		t.Fatal("two locators should fail")
	}
	if err := (Target{Ref: "e1"}).Validate(); err != nil {
		t.Fatal(err)
	}
	if err := (Target{Point: &Point{X: 1, Y: 2}}).Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestSafeDownloadName(t *testing.T) {
	for in, want := range map[string]string{
		"../evil.txt":    "evil.txt",
		`C:\tmp\x.pdf`:   "x.pdf",
		"":               "download",
		"report (1).csv": "report (1).csv",
	} {
		if got := safeDownloadName(in); got != want {
			t.Errorf("safeDownloadName(%q) = %q, want %q", in, got, want)
		}
	}
}
