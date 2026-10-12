package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

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
	"unassigned e0003": "\U000e0003",
}

// archeraReadableKeyFragment reports a run of 10+ key characters readable in
// out once everything but ASCII letters, digits and '-' is ignored.
func archeraReadableKeyFragment(out string) string {
	var b strings.Builder
	for _, r := range out {
		if r == '-' || r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' {
			b.WriteRune(r)
		}
	}
	s := b.String()
	for i := 0; i+10 <= len(archeraLongKey); i++ {
		if frag := archeraLongKey[i : i+10]; strings.Contains(s, frag) {
			return frag
		}
	}
	return ""
}

func TestArcheraLongKeyNeverReadableOnAnyPath(t *testing.T) {
	require.Len(t, archeraLongKey, 41)
	env := map[string]string{archeraEnvAPIKey: " " + archeraLongKey + "\t", archeraEnvOrgID: archeraTestOrg, archeraEnvPlanID: archeraTestPlan}
	for name, sep := range archeraInvisibleSeparators {
		for shape, echo := range map[string]string{
			"every": archeraSplitEverywhere(archeraLongKey, sep),
			"once":  archeraLongKey[:7] + sep + archeraLongKey[7:],
		} {
			check := func(path string, out, errOut string, err error) {
				t.Helper()
				require.Error(t, err, "%s %s %s", path, name, shape)
				got := out + errOut + err.Error()
				require.Empty(t, archeraReadableKeyFragment(got), "%s %s %s: %q", path, name, shape, got)
			}
			// HTTP: sweep pad so the pkg's 200-byte cut lands at every depth of the key.
			for pad := 0; pad <= 199; pad += 9 {
				msg := "bad key " + strings.Repeat("p", pad) + echo
				setupArchera(t, func(w http.ResponseWriter, _ *http.Request) {
					w.Header().Set("Retry-After", "7")
					w.WriteHeader(401)
					b, _ := json.Marshal(map[string]string{"message": msg})
					_, _ = w.Write(b)
				})
				out, errOut, err := runArchera(t, env)
				check(fmt.Sprintf("http pad %d", pad), out, errOut, err)
			}
			// Decode error echoing the rejected enum value.
			body := strings.Replace(archeraComparisonFixture(), `"payment_option":"no_upfront"`, `"payment_option":`+mustJSON(t, echo), 1)
			setupArchera(t, func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(body)) })
			out, errOut, err := runArchera(t, env)
			check("decode", out, errOut, err)
			msg := "bad key " + echo
			for kind, wrapped := range map[string]error{
				"transport": errors.New(msg),
				"deadline":  fmt.Errorf("%s: %w", msg, context.DeadlineExceeded),
				"canceled":  fmt.Errorf("%s: %w", msg, context.Canceled),
			} {
				setupArchera(t, okHandler)
				archeraHTTPClient = &http.Client{Transport: archeraFailingTransport{err: wrapped}}
				out, errOut, err := runArchera(t, env)
				check(kind, out, errOut, err)
			}
		}
	}
}
