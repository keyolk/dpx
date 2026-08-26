// Package fuzzy implements fzf-style subsequence matching with scoring,
// used for every list filter in dpx.
package fuzzy

import (
	"sort"
	"strings"
	"unicode"
)

// Match is a scored candidate with the positions that matched.
type Match struct {
	Index     int
	Score     int
	Positions []int
}

const (
	scoreMatch          = 16
	bonusBoundary       = 8  // match right after a separator (- _ . / space @)
	bonusCamel          = 7  // lower->upper transition
	bonusConsecutive    = 8  // adjacent to the previous match
	bonusFirstChar      = 12 // match at position 0
	penaltyGapStart     = -3
	penaltyGapExtension = -1
)

func isSep(r rune) bool {
	switch r {
	case '-', '_', '.', '/', ' ', '@', ':', ',', '(', ')', '[', ']':
		return true
	}
	return false
}

// Score matches pattern against text as a case-insensitive subsequence.
// It returns ok=false when the pattern is not a subsequence at all.
//
// The algorithm is greedy-forward then backtracks the tail: this finds the
// tightest trailing cluster (so "gh" prefers "GitHub" over "Group-hub") at a
// fraction of the cost of full Smith-Waterman, which matters because filters
// re-score every candidate on each keystroke.
func Score(text, pattern string) (score int, positions []int, ok bool) {
	if pattern == "" {
		return 0, nil, true
	}
	tr := []rune(text)
	pr := []rune(strings.ToLower(pattern))
	lower := make([]rune, len(tr))
	for i, r := range tr {
		lower[i] = unicode.ToLower(r)
	}

	// Forward pass: earliest possible match for each pattern rune.
	fwd := make([]int, 0, len(pr))
	ti := 0
	for _, p := range pr {
		for ti < len(lower) && lower[ti] != p {
			ti++
		}
		if ti == len(lower) {
			return 0, nil, false
		}
		fwd = append(fwd, ti)
		ti++
	}

	// Backward pass from the last forward match: pull each match as late as
	// possible without crossing the next one, tightening the cluster.
	pos := make([]int, len(pr))
	copy(pos, fwd)
	limit := fwd[len(fwd)-1]
	for i := len(pr) - 1; i >= 0; i-- {
		j := limit
		for j >= 0 && lower[j] != pr[i] {
			j--
		}
		if j < 0 || (i > 0 && j <= fwd[i-1]) {
			break
		}
		pos[i] = j
		limit = j - 1
	}

	prev := -2
	for i, p := range pos {
		score += scoreMatch
		switch {
		case p == 0:
			score += bonusFirstChar
		case isSep(tr[p-1]):
			score += bonusBoundary
		case unicode.IsUpper(tr[p]) && unicode.IsLower(tr[p-1]):
			score += bonusCamel
		}
		if i > 0 {
			if gap := p - prev - 1; gap > 0 {
				score += penaltyGapStart + penaltyGapExtension*(gap-1)
			} else {
				score += bonusConsecutive
			}
		}
		prev = p
	}
	// Shorter haystacks win ties: "dev" should rank "dev" above "developers".
	score -= len(tr) / 8
	return score, pos, true
}

// Filter scores every candidate against pattern and returns the matches,
// highest score first. An empty pattern returns every candidate in order.
func Filter(candidates []string, pattern string) []Match {
	out := make([]Match, 0, len(candidates))
	for i, c := range candidates {
		s, p, ok := Score(c, pattern)
		if !ok {
			continue
		}
		out = append(out, Match{Index: i, Score: s, Positions: p})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Score != out[j].Score {
			return out[i].Score > out[j].Score
		}
		return out[i].Index < out[j].Index
	})
	return out
}
