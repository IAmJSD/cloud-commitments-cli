package main

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// spHook answers the Savings Plan calls of a successful purchase.
func spHook() *hookTransport {
	h := &hookTransport{}
	h.handle = func(r *http.Request) (int, string) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/CreateSavingsPlan"):
			return http.StatusOK, `{"savingsPlanId":"sp-111"}`
		case strings.HasSuffix(r.URL.Path, "/DescribeSavingsPlans"):
			return http.StatusOK, `{"savingsPlans":[]}`
		}
		return http.StatusBadRequest, `{"__type":"ValidationException","message":"unexpected call"}`
	}
	return h
}

// failAuditWrites makes every audit write fail, as a full disk would.
func failAuditWrites(t *testing.T) {
	t.Helper()
	orig := writeAuditRecordFn
	writeAuditRecordFn = func(common.AuditRecord, string) error {
		return errors.New("no space left on device")
	}
	t.Cleanup(func() { writeAuditRecordFn = orig })
}

func auditCfg(t *testing.T) Config {
	t.Helper()
	dir := t.TempDir()
	return Config{AuditLog: filepath.Join(dir, "audit.jsonl"), CSVOutput: filepath.Join(dir, "report.csv")}
}

func TestExecuteAndReport_AuditWriteFailureStopsFurtherPurchases(t *testing.T) {
	t.Setenv("DISABLE_PURCHASE_DELAY", "true")
	out := captureAppLog(t)
	cfg := auditCfg(t)
	failAuditWrites(t)
	h := spHook()
	recs := []common.Recommendation{offeringSP("o-1", 1), offeringSP("o-2", 2), offeringSP("o-3", 3)}

	err := executeAndReport(context.Background(), hookedAWSConfig(h), recs, false, "run-1", cfg, nil)

	require.ErrorIs(t, err, errAuditTrailLost)
	assert.Equal(t, 1, h.count("/CreateSavingsPlan"), "no purchase may follow an unaudited one")
	assert.Positive(t, h.count("/DescribeSavingsPlans"), "the duplicate check ran before the purchase")
	assert.Contains(t, out.String(), "missing or not durable")
	assert.Contains(t, out.String(), "1 of 3")
	assert.Contains(t, out.String(), "sp-111")
	report, rerr := os.ReadFile(cfg.CSVOutput)
	require.NoError(t, rerr)
	assert.Contains(t, string(report), "sp-111", "the unaudited purchase still reaches the report")
}

func TestExecuteAndReport_AuditProbeFailureStopsBeforeBuying(t *testing.T) {
	t.Setenv("DISABLE_PURCHASE_DELAY", "true")
	captureAppLog(t)
	cfg := auditCfg(t)
	// The seam writes record 1, then the log path becomes a directory, which
	// fails the open with EISDIR even when the tests run as root.
	orig := writeAuditRecordFn
	writeAuditRecordFn = func(rec common.AuditRecord, path string) error {
		if err := orig(rec, path); err != nil {
			return err
		}
		if err := os.Rename(path, path+".1"); err != nil {
			return err
		}
		return os.Mkdir(path, 0o755)
	}
	t.Cleanup(func() { writeAuditRecordFn = orig })
	h := spHook()
	recs := []common.Recommendation{offeringSP("o-1", 1), offeringSP("o-2", 2)}

	err := executeAndReport(context.Background(), hookedAWSConfig(h), recs, false, "run-1", cfg, nil)

	require.ErrorIs(t, err, errAuditTrailLost)
	assert.Contains(t, err.Error(), "probe")
	assert.Equal(t, 1, h.count("/CreateSavingsPlan"), "purchase 2 must never be sent")
	rows := auditRows(t, cfg.AuditLog+".1")
	require.Len(t, rows, 1, "record 1 exists")
}

func TestProcessPurchaseLoop_AuditWriteFailureStopsFurtherPurchases(t *testing.T) {
	t.Setenv("DISABLE_PURCHASE_DELAY", "true")
	captureAppLog(t)
	cfg := auditCfg(t)
	failAuditWrites(t)
	recs := []common.Recommendation{validEC2Rec(), validEC2Rec(), validEC2Rec()}
	mockClient := &MockServiceClient{}
	mockClient.On("PurchaseCommitment", mock.Anything, mock.Anything, mock.Anything).
		Return(common.PurchaseResult{Success: true, CommitmentID: "ri-1"}, nil)

	results, err := processPurchaseLoop(context.Background(), recs, "us-east-1", false, mockClient, cfg, "run-1")

	require.ErrorIs(t, err, errAuditTrailLost)
	require.Len(t, results, 1, "the unaudited result is still returned")
	mockClient.AssertNumberOfCalls(t, "PurchaseCommitment", 1)
}

func TestRunCSVPurchases_AuditWriteFailureStopsAcrossRegionsAndServices(t *testing.T) {
	t.Setenv("DISABLE_PURCHASE_DELAY", "true")
	captureAppLog(t)
	cfg := auditCfg(t)
	failAuditWrites(t)
	h := spHook()
	ec2sp := offeringSP("o-3", 3)
	ec2sp.Service = common.ServiceSavingsPlansEC2Instance
	ec2sp.Details = &common.SavingsPlanDetails{PlanType: "EC2Instance", HourlyCommitment: 3, InstanceFamily: "m5", Region: "eu-west-1", OfferingID: "o-3"}
	ec2sp2 := ec2sp
	ec2sp2.Details = &common.SavingsPlanDetails{PlanType: "EC2Instance", HourlyCommitment: 4, InstanceFamily: "c5", Region: "eu-west-1", OfferingID: "o-4"}
	recs := []common.Recommendation{offeringSP("o-1", 1), offeringSP("o-2", 2), ec2sp, ec2sp2}

	err := runCSVPurchases(context.Background(), hookedAWSConfig(h), recs, false, cfg, "run-1")

	require.ErrorIs(t, err, errAuditTrailLost)
	assert.Equal(t, 1, h.count("/CreateSavingsPlan"), "whatever the map order, nothing is bought after the failure")
}

func TestProcessCSVService_AuditWriteFailureStopsRegionLoop(t *testing.T) {
	t.Setenv("DISABLE_PURCHASE_DELAY", "true")
	captureAppLog(t)
	cfg := auditCfg(t)
	failAuditWrites(t)
	h := spHook()
	byRegion := map[string][]common.Recommendation{
		"a": {offeringSP("o-1", 1)},
		"b": {offeringSP("o-2", 2)},
	}

	_, results, err := processCSVService(context.Background(), hookedAWSConfig(h), common.ServiceSavingsPlansCompute, byRegion, false, cfg, "run-1")

	require.ErrorIs(t, err, errAuditTrailLost)
	assert.Len(t, results, 1)
	assert.Equal(t, 1, h.count("/CreateSavingsPlan"))
}

func TestAuditWriteFailureOnDryRunDoesNotStop(t *testing.T) {
	captureAppLog(t)
	cfg := auditCfg(t)
	failAuditWrites(t)
	recs := []common.Recommendation{validEC2Rec(), validEC2Rec(), validEC2Rec()}

	results, err := processPurchaseLoop(context.Background(), recs, "us-east-1", true, &MockServiceClient{}, cfg, "run-1")

	require.NoError(t, err)
	assert.Len(t, results, 3)
}
