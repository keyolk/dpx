package fuzzy

import "testing"

func TestScoreSubsequenceOnly(t *testing.T) {
	if _, _, ok := Score("Github - Acme", "xyz"); ok {
		t.Fatal("non-subsequence pattern matched")
	}
	if _, _, ok := Score("Github - Acme", "ga"); !ok {
		t.Fatal("subsequence pattern did not match")
	}
}

func TestEmptyPatternMatchesEverything(t *testing.T) {
	s, pos, ok := Score("anything", "")
	if !ok || s != 0 || pos != nil {
		t.Fatalf("empty pattern: got (%d, %v, %v)", s, pos, ok)
	}
}

func TestPrefersWordBoundaries(t *testing.T) {
	// "ga" should rank the initials of two words above a mid-word hit.
	boundary, _, _ := Score("Github Acme", "ga")
	midword, _, _ := Score("gasoline", "gs")
	if boundary <= midword {
		t.Fatalf("boundary match (%d) did not beat midword (%d)", boundary, midword)
	}
}

func TestPrefersShorterOnTie(t *testing.T) {
	short, _, _ := Score("dev", "dev")
	long, _, _ := Score("developers-platform-team", "dev")
	if short <= long {
		t.Fatalf("short (%d) did not beat long (%d)", short, long)
	}
}

func TestConsecutiveBeatsScattered(t *testing.T) {
	consec, _, _ := Score("xxabc", "abc")
	scattered, _, _ := Score("axbxc", "abc")
	if consec <= scattered {
		t.Fatalf("consecutive (%d) did not beat scattered (%d)", consec, scattered)
	}
}

func TestPositionsAreAscendingAndCorrect(t *testing.T) {
	_, pos, ok := Score("Github-Acme", "ghac")
	if !ok {
		t.Fatal("expected match")
	}
	if len(pos) != 4 {
		t.Fatalf("want 4 positions, got %v", pos)
	}
	runes := []rune("Github-Acme")
	want := "ghac"
	for i, p := range pos {
		if i > 0 && p <= pos[i-1] {
			t.Fatalf("positions not ascending: %v", pos)
		}
		if got := toLower(runes[p]); got != rune(want[i]) {
			t.Fatalf("position %d points at %q, want %q", i, got, want[i])
		}
	}
}

func toLower(r rune) rune {
	if r >= 'A' && r <= 'Z' {
		return r + 32
	}
	return r
}

func TestFilterSortsByScore(t *testing.T) {
	cands := []string{"gasoline", "Github Acme", "gamma-sloth"}
	got := Filter(cands, "ga")
	if len(got) != 3 {
		t.Fatalf("want 3 matches, got %d", len(got))
	}
	for i := 1; i < len(got); i++ {
		if got[i-1].Score < got[i].Score {
			t.Fatalf("results not sorted by score: %v", got)
		}
	}
}

func TestFilterEmptyPatternKeepsOrder(t *testing.T) {
	cands := []string{"c", "a", "b"}
	got := Filter(cands, "")
	if len(got) != 3 {
		t.Fatalf("want 3, got %d", len(got))
	}
	for i, m := range got {
		if m.Index != i {
			t.Fatalf("order changed: %v", got)
		}
	}
}
