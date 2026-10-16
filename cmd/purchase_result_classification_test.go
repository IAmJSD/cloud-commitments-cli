package main

import (
	"context"
	"errors"
	"testing"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/LeanerCloud/cloud-commitments-go/pkg/provider"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

// purchaseClassificationCases covers how a provider's PurchaseCommitment
// return is classified (issue #1900): a result is a success only when
// Success is true AND Error is nil.
func purchaseClassificationCases() []struct {
	name       string
	result     common.PurchaseResult
	err        error
	wantStatus string
	wantMarker string
} {
	providerErr := errors.New("provider reported partial failure")
	return []struct {
		name       string
		result     common.PurchaseResult
		err        error
		wantStatus string
		wantMarker string
	}{
		{"success", common.PurchaseResult{Success: true, CommitmentID: "ri-ok"}, nil, "success", "✅"},
		{"success flag with provider error", common.PurchaseResult{Success: true, CommitmentID: "ri-err", Error: providerErr}, nil, "error", "❌"},
		{"success false", common.PurchaseResult{Success: false, Error: providerErr}, nil, "error", "❌"},
		{"separate go error", common.PurchaseResult{Success: true, CommitmentID: "ri-goerr"}, errors.New("api call failed"), "error", "❌"},
	}
}

func TestPurchaseSingleRec_ClassifiesErroredProviderResultAsFailure(t *testing.T) {
	t.Setenv("DISABLE_PURCHASE_DELAY", "true")
	for _, tc := range purchaseClassificationCases() {
		t.Run(tc.name, func(t *testing.T) {
			client := &MockServiceClient{}
			client.On("PurchaseCommitment", mock.Anything, mock.Anything, mock.Anything).Return(tc.result, tc.err)
			oldFactory := newServiceClient
			newServiceClient = func(common.ServiceType, aws.Config) provider.ServiceClient { return client }
			t.Cleanup(func() { newServiceClient = oldFactory })

			cfg := Config{CallerAccountID: defaultTestAccount}
			var status string
			out := captureAppOutput(t, func() {
				_, status = purchaseSingleRec(context.Background(), aws.Config{}, validEC2Rec(), 1, false, cfg)
			})

			client.AssertNumberOfCalls(t, "PurchaseCommitment", 1)
			assert.Equal(t, tc.wantStatus, status)
			assert.Contains(t, out, tc.wantMarker)
			if tc.wantStatus == "error" {
				assert.NotContains(t, out, "✅")
			}
		})
	}
}

func TestRunOnePurchase_ClassifiesErroredProviderResultAsFailure(t *testing.T) {
	for _, tc := range purchaseClassificationCases() {
		t.Run(tc.name, func(t *testing.T) {
			client := &MockServiceClient{}
			client.On("PurchaseCommitment", mock.Anything, mock.Anything, mock.Anything).Return(tc.result, tc.err)
			rec := validEC2Rec()
			cfg := Config{CallerAccountID: defaultTestAccount}

			var status string
			out := captureAppOutput(t, func() {
				var result common.PurchaseResult
				result, status, _ = runOnePurchase(context.Background(), rec, rec.Region, rec.Region, 1, false, client, cfg)
				logPurchaseOutcome(result)
			})

			assert.Equal(t, tc.wantStatus, status)
			assert.Contains(t, out, tc.wantMarker)
			if tc.wantStatus == "error" {
				assert.NotContains(t, out, "✅")
			}
		})
	}
}
