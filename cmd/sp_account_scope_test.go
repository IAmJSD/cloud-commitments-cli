package main

import (
	"context"
	"testing"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

func TestValidateSPAccountScope(t *testing.T) {
	orig := toolCfg.SPAccountScope
	t.Cleanup(func() { toolCfg.SPAccountScope = orig })
	for scope, wantErr := range map[string]bool{"": false, "linked": false, "payer": false, "PAYER": false, "org": true} {
		toolCfg.SPAccountScope = scope
		err := validateSPAccountScope()
		if wantErr {
			require.Error(t, err, scope)
			assert.Contains(t, err.Error(), "invalid sp-account-scope")
		} else {
			require.NoError(t, err, scope)
		}
	}
}

func TestFetchRecommendationsForRegion_PassesSPAccountScope(t *testing.T) {
	captureAppLog(t)
	client := &MockRecommendationsClient{}
	var got *common.RecommendationParams
	client.On("GetRecommendations", mock.Anything, mock.Anything).
		Run(func(args mock.Arguments) { got = args.Get(1).(*common.RecommendationParams) }).
		Return([]common.Recommendation{}, nil)

	fetchRecommendationsForRegion(context.Background(), client, common.ServiceSavingsPlansCompute, spAPIRegion,
		Config{TermYears: 1, SPAccountScope: "payer"})

	require.NotNil(t, got)
	assert.Equal(t, "payer", got.SPAccountScope)
}
