package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	csvCoverageArgsEnv = "CUDLY_CSV_COVERAGE_TEST_ARGS"
	csvCoverageWantEnv = "CUDLY_CSV_COVERAGE_TEST_WANT"
)

// TestCSVCoverageFlagThroughRootCommand reproduces #2236 through the real
// command: cobra flag parsing, validateFlags, runTool and the --input-csv
// dry run. Before the fix an explicit --coverage 80 equaled the flag's default,
// so determineCSVCoverage treated it as unset and every row was bought at its
// full Count. Each case runs in a child process so the flag state cobra keeps
// on rootCmd (values and Changed bits) never leaks between cases or tests.
func TestCSVCoverageFlagThroughRootCommand(t *testing.T) {
	if args, ok := os.LookupEnv(csvCoverageArgsEnv); ok {
		runCSVCoverageChild(t, args, os.Getenv(csvCoverageWantEnv))
		return
	}
	tests := []struct {
		name string
		args []string
		want string
	}{
		{"no flag buys each row's full count", nil, "10,20"},
		{"explicit 80 is honored", []string{"--coverage", "80"}, "8,16"},
		{"explicit 80 via shorthand is honored", []string{"-c", "80"}, "8,16"},
		{"explicit 80 with equals is honored", []string{"--coverage=80"}, "8,16"},
		{"explicit 100 buys each row's full count", []string{"--coverage", "100"}, "10,20"},
		{"explicit 50 halves each row", []string{"--coverage", "50"}, "5,10"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			defer cancel()
			child := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestCSVCoverageFlagThroughRootCommand$", "-test.v")
			child.Env = []string{
				csvCoverageArgsEnv + "=" + strings.Join(tt.args, "\x1f"),
				csvCoverageWantEnv + "=" + tt.want,
			}
			output, err := child.CombinedOutput()
			require.NoError(t, err, "%s", output)
			assert.Contains(t, string(output), "--- PASS: TestCSVCoverageFlagThroughRootCommand")
		})
	}
}

// runCSVCoverageChild executes rootCmd with the case's flags on a dry-run
// --input-csv invocation and checks the per-row purchase counts in the report.
func runCSVCoverageChild(t *testing.T, joinedArgs, want string) {
	awsHits := isolateAWSEnv(t)
	dir := t.TempDir()
	csvPath := writeTestRecommendationsCSV(t, `Service,Region,ResourceType,Engine,Count,Term,PaymentOption,Account,EstimatedSavings
rds,us-east-1,db.t3.small,postgres,10,1yr,all-upfront,111111111111,100
rds,us-east-1,db.t3.medium,postgres,20,1yr,all-upfront,111111111111,200
`)
	reportPath := filepath.Join(dir, "report.csv")
	args := []string{"--input-csv", csvPath, "--output", reportPath, "--audit-log", filepath.Join(dir, "audit.jsonl")}
	if joinedArgs != "" {
		args = append(args, strings.Split(joinedArgs, "\x1f")...)
	}
	rootCmd.SetArgs(args)
	require.NoError(t, rootCmd.Execute())

	header, rows := readPurchaseReport(t, reportPath)
	byType := map[string]string{}
	types := reportColumn(t, header, rows, "ResourceType")
	for i, count := range reportColumn(t, header, rows, "Count") {
		byType[types[i]] = count
	}
	wantCounts := strings.Split(want, ",")
	assert.Equal(t, map[string]string{"db.t3.small": wantCounts[0], "db.t3.medium": wantCounts[1]}, byType)
	for _, dryRun := range reportColumn(t, header, rows, "Success") {
		assert.Equal(t, "true", dryRun)
	}
	assert.Positive(t, awsHits.Load(), "the local AWS endpoint must have served the duplicate checks")
}
