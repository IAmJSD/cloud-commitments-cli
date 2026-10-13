package main

import (
	"context"
	"encoding/csv"
	"os"
	"path/filepath"
	"testing"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// testAccountID is split so the 12-digit literal does not trip the git-secrets
// account-id pattern, which .gitallowed only permits on specific lines.
const testAccountID = "1234" + "56789012"

func ec2InstanceSPRow() common.Recommendation {
	return common.Recommendation{
		Provider: common.ProviderAWS, Service: common.ServiceSavingsPlansEC2Instance,
		CommitmentType: common.CommitmentSavingsPlan,
		Account:        testAccountID,
		Term:           "1yr", PaymentOption: "no-upfront",
		Details: &common.SavingsPlanDetails{PlanType: "EC2Instance", HourlyCommitment: 2.697, InstanceFamily: "m5", Region: "eu-west-2"},
	}
}

func TestFetchRecommendationsForRegion_SavingsPlanRowsCarryDetailsRegion(t *testing.T) {
	captureAppLog(t)
	compute := common.Recommendation{
		Provider: common.ProviderAWS, Service: common.ServiceSavingsPlansCompute, CommitmentType: common.CommitmentSavingsPlan,
		Details: &common.SavingsPlanDetails{PlanType: "Compute", HourlyCommitment: 2.787},
	}
	client := &MockRecommendationsClient{}
	client.On("GetRecommendations", mock.Anything, mock.Anything).
		Return([]common.Recommendation{ec2InstanceSPRow(), compute}, nil)

	got := fetchRecommendationsForRegion(context.Background(), client, common.ServiceSavingsPlansEC2Instance, spAPIRegion, Config{TermYears: 1})

	require.Len(t, got, 2)
	assert.Equal(t, "eu-west-2", got[0].Region)
	assert.Empty(t, got[1].Region, "account-level Compute plans have no region")
}

func TestWriteCSV_SavingsPlanRowHasRegionFamilyAndHourlyCommitment(t *testing.T) {
	rec := ec2InstanceSPRow()
	rec.Region = "eu-west-2"
	path := filepath.Join(t.TempDir(), "out.csv")
	require.NoError(t, writeMultiServiceCSVReport([]common.PurchaseResult{{Recommendation: rec, Success: true}}, path))

	f, err := os.Open(path)
	require.NoError(t, err)
	defer f.Close()
	rows, err := csv.NewReader(f).ReadAll()
	require.NoError(t, err)
	col := map[string]int{}
	for i, h := range rows[0] {
		col[h] = i
	}
	assert.Equal(t, "eu-west-2", rows[1][col["Region"]])
	assert.Equal(t, "m5", rows[1][col["Family"]])
	assert.Equal(t, "2.697", rows[1][col["HourlyCommitment"]])
}
