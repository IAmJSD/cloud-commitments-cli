package main

import (
	"errors"
	"fmt"
	"math"
	"net/http"
	"os"
	"regexp"
	"strings"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/insurance"
	"github.com/spf13/cobra"
)

// Archera settings come from the environment, read only inside RunE. The API
// key has no flag by design.
const (
	archeraEnvAPIKey = "ARCHERA_API_KEY" // #nosec G101 -- environment variable name, not a credential
	archeraEnvOrgID  = "ARCHERA_ORG_ID"
	archeraEnvPlanID = "ARCHERA_PLAN_ID"
)

// archeraHTTPClient is nil in production, which selects the hardened default
// client inside pkg/insurance. Tests replace it with a fixed-origin transport.
// It never holds the key; the key lives only in RunE locals.
var archeraHTTPClient *http.Client

var archeraOpts struct {
	OrgID  string
	PlanID string
	Format string
}

var archeraComparisonCmd = &cobra.Command{
	Use:   "archera-comparison",
	Short: "Show a read-only Archera commitment plan comparison",
	Long: `Fetch and print the comparison for one Archera commitment plan.

This command is read-only: it never purchases, binds or submits anything, and
it is independent of --purchase. It makes no Archera request unless it is run.

Configuration (default off; there is no flag for the API key):
  ARCHERA_API_KEY   Archera API key (environment only)
  ARCHERA_ORG_ID    Archera organization UUID (or --org-id)
  ARCHERA_PLAN_ID   Archera commitment plan UUID (or --plan-id)`,
	Args:          cobra.NoArgs,
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE:          runArcheraComparison,
}

func init() {
	rootCmd.AddCommand(archeraComparisonCmd)
	archeraComparisonCmd.Flags().StringVar(&archeraOpts.OrgID, "org-id", "", "Archera organization UUID (default: $"+archeraEnvOrgID+")")
	archeraComparisonCmd.Flags().StringVar(&archeraOpts.PlanID, "plan-id", "", "Archera commitment plan UUID (default: $"+archeraEnvPlanID+")")
	archeraComparisonCmd.Flags().StringVar(&archeraOpts.Format, "format", "table", "Output format: table or json")
}

// archeraSetting returns the flag value, else the environment value, trimmed.
func archeraSetting(flagValue, envName string) string {
	if v := strings.TrimSpace(flagValue); v != "" {
		return v
	}
	return strings.TrimSpace(os.Getenv(envName))
}

func runArcheraComparison(cmd *cobra.Command, _ []string) error {
	format := archeraOpts.Format
	if format != "table" && format != "json" {
		return errors.New("--format must be table or json")
	}
	key, orgID, planID, err := archeraSettings()
	if err != nil {
		return err
	}

	// Config and client stay local to this call; never store them in a
	// package variable or struct.
	client, err := insurance.NewClient(insurance.Config{APIKey: key, OrgID: orgID}, archeraHTTPClient)
	if err != nil {
		return err
	}
	comparison, err := client.Comparison(cmd.Context(), insurance.ComparisonRequest{PlanID: planID})
	if err != nil {
		return archeraSafeError(err, key)
	}
	dto := buildArcheraDTO(comparison)
	if format == "json" {
		return renderArcheraJSON(cmd.OutOrStdout(), dto)
	}
	return renderArcheraTable(cmd.OutOrStdout(), dto)
}

// archeraSettings reads the key, org ID and plan ID, naming (never echoing)
// the first missing setting. The key is returned to RunE only.
func archeraSettings() (key, orgID, planID string, err error) {
	key = strings.TrimSpace(os.Getenv(archeraEnvAPIKey))
	if key == "" {
		return "", "", "", fmt.Errorf("%s is not set (read from the environment only)", archeraEnvAPIKey)
	}
	orgID = archeraSetting(archeraOpts.OrgID, archeraEnvOrgID)
	if orgID == "" {
		return "", "", "", fmt.Errorf("archera org ID is not set (--org-id or %s)", archeraEnvOrgID)
	}
	planID = archeraSetting(archeraOpts.PlanID, archeraEnvPlanID)
	if planID == "" {
		return "", "", "", fmt.Errorf("archera plan ID is not set (--plan-id or %s)", archeraEnvPlanID)
	}
	return key, orgID, planID, nil
}

var archeraQuotedEscapeRE = regexp.MustCompile(`\\(?:u[0-9a-fA-F]{4}|U[0-9a-fA-F]{8}|x[0-9a-fA-F]{2})`)

// archeraWithheld replaces vendor text in which the key is still readable
// after masking.
const archeraWithheld = "[message withheld: contained the API key]"

// archeraSafeError renders a client error as bounded, terminal-safe text.
// HTTP errors keep their status and Retry-After. Decode errors echo the
// rejected vendor value. Both are vendor-controlled, so the order is: clean
// the text (controls, format and separator characters removed, which would
// otherwise let a key split by U+200B slip past an exact-match mask), mask the
// trimmed key, check the full text for a key hidden behind characters the
// clean leaves in place (combining marks, variation selectors, fillers,
// invalid bytes) and withhold it if one is found, then cap.
func archeraSafeError(err error, key string) error {
	key = strings.TrimSpace(key)
	msg, suffix := err.Error(), ""
	var httpErr *insurance.HTTPError
	isHTTP := errors.As(err, &httpErr)
	if isHTTP && httpErr.RetryAfter > 0 {
		suffix = fmt.Sprintf(" (retry after %s; not retried automatically)", httpErr.RetryAfter)
	}
	// pkg decode errors quote the rejected value with %q, which spells control
	// and format characters as literal \uXXXX text; drop those escapes too so
	// they cannot split the key.
	msg = archeraQuotedEscapeRE.ReplaceAllString(archeraSanitizeN(msg+suffix, math.MaxInt), "")
	if key != "" {
		msg = strings.ReplaceAll(msg, key, "[redacted]")
		if archeraKeyReadable(msg, key) {
			msg = archeraWithheld
			if isHTTP {
				msg = fmt.Sprintf("archera API returned HTTP %d: %s%s", httpErr.StatusCode, archeraWithheld, suffix)
			}
		}
	}
	return errors.New(archeraSanitizeN(msg, archeraMaxErrorSize))
}

// archeraFragmentLen is the longest key fragment that may remain readable in
// vendor text. The pkg cuts HTTP messages at 200 bytes before the CLI sees
// them, so a split key can arrive as a prefix rather than whole.
const archeraFragmentLen = 8

// archeraSkeleton keeps only the runes that occur in the key, so any character
// a vendor inserts to hide the key (known or not, visible or not) drops out.
// alphabet is the rune set of the trimmed key.
func archeraSkeleton(s string, alphabet map[rune]bool) string {
	var b strings.Builder
	for _, r := range s {
		if alphabet[r] {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// archeraKeyReadable reports whether msg shows a run of at least
// min(len(key), archeraFragmentLen) consecutive key characters once every
// non-key character is dropped.
func archeraKeyReadable(msg, key string) bool {
	alphabet := make(map[rune]bool, len(key))
	for _, r := range key {
		alphabet[r] = true
	}
	ks := []rune(archeraSkeleton(key, alphabet))
	ms := archeraSkeleton(msg, alphabet)
	n := min(len(ks), archeraFragmentLen)
	for i := 0; i+n <= len(ks); i++ {
		if strings.Contains(ms, string(ks[i:i+n])) {
			return true
		}
	}
	return false
}
