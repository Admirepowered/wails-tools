package settings

import "testing"

func TestResolveSubscriptionUserAgent(t *testing.T) {
	if got := ResolveSubscriptionUserAgent(""); got == "" || got[:7] != "Mozilla" {
		t.Fatalf("default UA = %q", got)
	}
	if got := ResolveSubscriptionUserAgent("quantumultx"); got[:9] != "Quantumul" {
		t.Fatalf("quantumultx UA = %q", got)
	}
	if got := ResolveSubscriptionUserAgent("Quantumult%20X/9"); got != "Quantumult%20X/9" {
		t.Fatalf("literal UA = %q", got)
	}
	if got := ResolveSubscriptionUserAgent("clash"); got == "" {
		t.Fatal("clash UA empty")
	}
}
