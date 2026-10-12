package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A 41-character synthetic key. The pinned pkg cuts HTTP error messages at 200
// bytes before the CLI sees them, and a key this long split by an invisible
// character spans that cut, which the 23-byte archeraTestKey never does.
const archeraLongKey = "PRB-sYnTh3t1c-QZ9x-7Kd2-mW8v-Lp4n-Ht6r-00"

// archeraInvisibleSeparators are characters a terminal shows as nothing or as
// a blank, including ones outside every Unicode category list (unassigned
// default-ignorables) and U+2800.
var archeraInvisibleSeparators = map[string]string{
	"zwsp": "\u200b", "word joiner": "\u2060", "vs16": "\ufe0f", "vs17": "\U000e0100",
	"cgj": "\u034f", "acute": "\u0301", "enclosing circle": "\u20dd",
	"hangul choseong filler": "\u115f", "hangul jungseong filler": "\u1160",
	"hangul filler": "\u3164", "halfwidth hangul filler": "\uffa0",
	"space": " ", "nbsp": "\u00a0", "ideographic space": "\u3000", "replacement char": "\ufffd",
	"khmer inherent vowel": "\u17b4", "mongolian vowel separator": "\u180e",
	"tag letter": "\U000e0041", "unassigned fff0": "\ufff0", "unassigned 2065": "\u2065",
	"unassigned e0002": "\U000e0002", "braille blank": "\u2800", "zwnj": "\u200c",
	"soft hyphen": "\u00ad", "lri": "\u2066", "literal escape": `\u200b`,
	"invalid utf-8": "\xff", "unassigned fff8": "\ufff8", "unassigned e001f": "\U000e001f",
	"unassigned e0003":     "\U000e0003",
	"triple braille blank": "\u2800\u2800\u2800", "triple unassigned 2065": "\u2065\u2065\u2065",
}

// archeraKeyAlphabetSeparators are characters that occur in archeraLongKey
// itself, so an allow-list skeleton keeps them between the key characters.
var archeraKeyAlphabetSeparators = []string{"-", "x", "0", "--", "---", "0x0", "-- -", "-x0-9"}

// archeraFullwidth maps printable ASCII to its fullwidth form.
func archeraFullwidth(s string) string {
	return strings.Map(func(r rune) rune {
		if r >= 0x21 && r <= 0x7e {
			return r + 0xfee0
		}
		return r
	}, s)
}

// archeraReadableKeyFragment reports a run of 8+ key characters readable in
// out, ignoring case and fullwidth forms and everything but ASCII letters,
// digits and '-'. 8 is the command's own threshold.
func archeraReadableKeyFragment(out string) string {
	var b strings.Builder
	for _, r := range out {
		if r >= 0xff01 && r <= 0xff5e {
			r -= 0xfee0
		}
		if r == '-' || r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' {
			b.WriteRune(r)
		}
	}
	s := strings.ToLower(b.String())
	key := strings.ToLower(archeraLongKey)
	for i := 0; i+8 <= len(key); i++ {
		if frag := key[i : i+8]; strings.Contains(s, frag) {
			return frag
		}
	}
	return ""
}

// forArcheraErrorPaths drives every error path with msg and calls check with
// each result. pads are the padding lengths swept across the pkg's 200-byte
// HTTP cut.
func forArcheraErrorPaths(t *testing.T, echo string, pads []int, check func(path, got string)) {
	t.Helper()
	env := map[string]string{archeraEnvAPIKey: " " + archeraLongKey + "\t", archeraEnvOrgID: archeraTestOrg, archeraEnvPlanID: archeraTestPlan}
	done := func(path string, out, errOut string, err error) {
		t.Helper()
		require.Error(t, err, path)
		check(path, out+errOut+err.Error())
	}
	for _, pad := range pads {
		msg := "bad key " + strings.Repeat("p", pad) + echo
		setupArchera(t, func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Retry-After", "7")
			w.WriteHeader(401)
			b, _ := json.Marshal(map[string]string{"message": msg})
			_, _ = w.Write(b)
		})
		out, errOut, err := runArchera(t, env)
		done(fmt.Sprintf("http pad %d", pad), out, errOut, err)
	}
	body := strings.Replace(archeraComparisonFixture(), `"payment_option":"no_upfront"`, `"payment_option":`+mustJSON(t, echo), 1)
	setupArchera(t, func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(body)) })
	out, errOut, err := runArchera(t, env)
	done("decode", out, errOut, err)
	msg := "bad key " + echo
	for kind, wrapped := range map[string]error{
		"transport": errors.New(msg),
		"deadline":  fmt.Errorf("%s: %w", msg, context.DeadlineExceeded),
		"canceled":  fmt.Errorf("%s: %w", msg, context.Canceled),
	} {
		setupArchera(t, okHandler)
		archeraHTTPClient = &http.Client{Transport: archeraFailingTransport{err: wrapped}}
		out, errOut, err := runArchera(t, env)
		done(kind, out, errOut, err)
	}
}

func TestArcheraLongKeyNeverReadableOnAnyPath(t *testing.T) {
	require.Len(t, archeraLongKey, 41)
	var pads []int
	for pad := 0; pad <= 199; pad += 9 {
		pads = append(pads, pad)
	}
	for name, sep := range archeraInvisibleSeparators {
		for shape, echo := range map[string]string{
			"every": archeraSplitEverywhere(archeraLongKey, sep),
			"once":  archeraLongKey[:7] + sep + archeraLongKey[7:],
		} {
			forArcheraErrorPaths(t, echo, pads, func(path, got string) {
				require.Empty(t, archeraReadableKeyFragment(got), "%s %s %s: %q", path, name, shape, got)
			})
		}
	}
}

// Case-changed and fullwidth echoes are the key to a reader, and so are keys
// spaced out by characters taken from the key itself.
func TestArcheraLongKeyVisibleDisguisesAreWithheld(t *testing.T) {
	echoes := map[string]string{
		"lower":     strings.ToLower(archeraLongKey),
		"upper":     strings.ToUpper(archeraLongKey),
		"fullwidth": archeraFullwidth(archeraLongKey),
	}
	for _, sep := range archeraKeyAlphabetSeparators {
		echoes["sep "+sep] = archeraSplitEverywhere(archeraLongKey, sep)
		echoes["fullwidth sep "+sep] = archeraSplitEverywhere(archeraFullwidth(archeraLongKey), sep)
	}
	for name, echo := range echoes {
		forArcheraErrorPaths(t, echo, []int{0}, func(path, got string) {
			if path == "decode" {
				// pkg/insurance no longer echoes vendor values in decode errors.
				assert.Empty(t, archeraReadableKeyFragment(got), "%s %s: %q", path, name, got)
				return
			}
			assert.Contains(t, got, archeraWithheld, "%s %s: %q", path, name, got)
		})
	}
}

// Exactly 8 consecutive key characters are withheld, 7 are shown; the first
// and last 8 count as much as any other.
func TestArcheraKeyFragmentBoundary(t *testing.T) {
	spaced := func(frag string) error {
		return archeraSafeError(errors.New("A: "+archeraSplitEverywhere(frag, "\u2800")), archeraLongKey)
	}
	k := archeraLongKey
	for name, frag := range map[string]string{"middle 8": k[10:18], "first 8": k[:8], "last 8": k[len(k)-8:]} {
		assert.Contains(t, spaced(frag).Error(), archeraWithheld, name)
	}
	for name, frag := range map[string]string{"middle 7": k[10:17], "first 7": k[:7], "last 7": k[len(k)-7:]} {
		assert.NotContains(t, spaced(frag).Error(), archeraWithheld, name)
	}
}

// The irregular-insertion rule is bounded: key characters 2 apart read as the
// key, 3 apart do not. Uses an 8-character key so there is a single window.
func TestArcheraKeyGapBound(t *testing.T) {
	const key = "abcdefgh"
	two := archeraSafeError(errors.New("A: abc"+"aa"+"defgh"), key).Error()
	assert.Contains(t, two, archeraWithheld)
	three := archeraSafeError(errors.New("A: abc"+"aaa"+"defgh"), key).Error()
	assert.NotContains(t, three, archeraWithheld)
}

// The matcher is linear and looks only at what could be displayed: a hostile
// 1 MB rejected value must not stall the command.
func TestArcheraHostileLargeDecodeValueIsBounded(t *testing.T) {
	const n = 1 << 20
	value := strings.Repeat("p", n/2) + strings.Repeat("r", n/2)
	body := strings.Replace(archeraComparisonFixture(), `"payment_option":"no_upfront"`, `"payment_option":`+mustJSON(t, value), 1)
	setupArchera(t, func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(body)) })
	env := map[string]string{archeraEnvAPIKey: archeraLongKey, archeraEnvOrgID: archeraTestOrg, archeraEnvPlanID: archeraTestPlan}
	start := time.Now()
	_, _, err := runArchera(t, env)
	elapsed := time.Since(start)
	require.Error(t, err)
	assert.LessOrEqual(t, len(err.Error()), archeraMaxErrorSize)
	assert.Less(t, elapsed, time.Second)
}

// Only text that could be displayed (plus a small slack for a key cut by the
// cap) is checked: a key echoed far beyond the cap changes nothing.
func TestArcheraKeyBeyondCheckWindowIsIgnored(t *testing.T) {
	far := strings.Repeat("A", archeraMaxErrorSize+archeraCheckSlack+64) + archeraSplitEverywhere(archeraLongKey, "\u2800")
	got := archeraSafeError(errors.New(far), archeraLongKey).Error()
	assert.NotContains(t, got, archeraWithheld)
	assert.Equal(t, strings.Repeat("A", archeraMaxErrorSize), got)
}

// A key spaced by the longest separator and cut by the cap is still withheld,
// so the look-ahead slack covers a whole window of such a key.
func TestArcheraKeySpacedByLongestSeparatorAtCapIsWithheld(t *testing.T) {
	sep := strings.Repeat("-", archeraMaxSep)
	msg := strings.Repeat("A", archeraMaxErrorSize-4) + archeraSplitEverywhere(archeraLongKey, sep)
	got := archeraSafeError(errors.New(msg), archeraLongKey).Error()
	assert.Equal(t, archeraWithheld, got)
}

// A key with an inner space or a format character is matched on what a reader
// sees of it.
func TestArcheraKeyWithInnerSpaceOrFormatRune(t *testing.T) {
	for name, key := range map[string]string{"inner space": "abcd efgh ijkl", "format rune": "abcdefg\u200bhijklmn"} {
		got := archeraSafeError(errors.New("A: abcd\u2800efgh\u2800ijkl and abcdefghijklmn"), key).Error()
		assert.Contains(t, got, archeraWithheld, name)
	}
}

// Ordinary vendor text must survive: the gap rule only fires on the key.
func TestArcheraOrdinaryVendorTextIsNotWithheld(t *testing.T) {
	msgs := []string{
		"invalid credentials for the account; request id 4821-aa; HTTP 403 forbidden",
		"organization not found: check the org id and the plan id",
		"hypothetical_totals[0]: payment_option: unsupported value \"partial_upfront_x\"",
	}
	for _, key := range []string{archeraLongKey, archeraTestKey} {
		for _, m := range msgs {
			assert.Equal(t, m, archeraSafeError(errors.New(m), key).Error(), key)
		}
	}
}
