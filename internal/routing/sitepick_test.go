package routing

import (
	"reflect"
	"testing"
)

func TestMatchSiteOffersTheRegistrableDomainFirst(t *testing.T) {
	m := MatchSite("static.news.bbc.co.uk", nil)
	want := []string{"bbc.co.uk", "news.bbc.co.uk", "static.news.bbc.co.uk"}
	if !reflect.DeepEqual(m.Options, want) || m.Suggested != "bbc.co.uk" {
		t.Fatalf("got options %v suggested %q", m.Options, m.Suggested)
	}
	if m.Listed || m.Service != "" {
		t.Fatalf("unexpected match: %+v", m)
	}
}

func TestMatchSiteNormalisesWhatABrowserHandsOver(t *testing.T) {
	if got := MatchSite("WWW.Example.COM.:8443", nil).Host; got != "www.example.com" {
		t.Fatalf("got %q", got)
	}
	if got := MatchSite("[2001:db8::1]", nil).Host; got != "2001:db8::1" {
		t.Fatalf("got %q", got)
	}
	if got := MatchSite("evil.com/path", nil).Host; got != "" {
		t.Fatalf("a path is not a host, got %q", got)
	}
}

func TestMatchSiteFindsTheCoveringEntry(t *testing.T) {
	m := MatchSite("www.youtube.com", []string{"example.org", "https://YouTube.com/"})
	if !m.Listed || m.ListedBy != "https://YouTube.com/" {
		t.Fatalf("got %+v", m)
	}
	// A sibling sharing a suffix string is not covered.
	if MatchSite("notyoutube.com", []string{"youtube.com"}).Listed {
		t.Fatal("notyoutube.com must not match youtube.com")
	}
	if !MatchSite("91.108.4.10", []string{"91.108.4.0/22"}).Listed {
		t.Fatal("an address inside a listed range is listed")
	}
}

// A catalogue service's host adds the whole service - Gemini lives on a
// subdomain of google.com, and adding the registrable domain there would
// route all of Google.
func TestAddingAServiceHostAddsTheWholeService(t *testing.T) {
	got, ok := AddSite([]string{"example.org"}, "gemini.google.com", "")
	if !ok {
		t.Fatal("refused")
	}
	for _, d := range []string{"gemini.google.com", "aistudio.google.com", "generativelanguage.googleapis.com"} {
		if !containsFold(got, d) {
			t.Fatalf("%s missing from %v", d, got)
		}
	}
	if containsFold(got, "google.com") {
		t.Fatalf("added all of Google: %v", got)
	}
}

func TestAddSiteUsesTheChosenOptionAndNothingElse(t *testing.T) {
	got, ok := AddSite(nil, "a.b.example.com", "b.example.com")
	if !ok || !reflect.DeepEqual(got, []string{"b.example.com"}) {
		t.Fatalf("got %v %v", got, ok)
	}
	if _, ok := AddSite(nil, "a.example.com", "other.com"); ok {
		t.Fatal("an entry that isn't one of the host's options must be refused")
	}
	again, _ := AddSite([]string{"example.com"}, "www.example.com", "")
	if len(again) != 1 {
		t.Fatalf("added a duplicate: %v", again)
	}
}

func TestRemoveSiteTakesTheServiceWithIt(t *testing.T) {
	yt := MatchSite("youtube.com", nil).ServiceDomains
	sites := append([]string{"example.org"}, yt...)
	got := RemoveSite(sites, "m.youtube.com")
	if !reflect.DeepEqual(got, []string{"example.org"}) {
		t.Fatalf("got %v", got)
	}
	// Only part of a service listed: just the covering entry goes.
	got = RemoveSite([]string{"youtube.com", "ytimg.com", "rutracker.org"}, "www.youtube.com")
	if !reflect.DeepEqual(got, []string{"ytimg.com", "rutracker.org"}) {
		t.Fatalf("got %v", got)
	}
}
