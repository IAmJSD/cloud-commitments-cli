package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// testSPSecret is the fake client secret returned by the mocked provisioner.
// It is long and distinctive so any partial leak is detectable.
const (
	testSPSecret = "Zq8~fakeSPsecret.DoNotPrint_7f3a9c2e1b4d6082"
	testSPAppID  = "99999999-8888-7777-6666-555555555555"
)

// captureAllOutput runs fn with os.Stdout, os.Stderr and the standard logger
// redirected, and returns everything written to any of them.
func captureAllOutput(t *testing.T, fn func()) string {
	t.Helper()

	origStdout, origStderr := os.Stdout, os.Stderr
	origLogOut := log.Writer()

	r, w, err := os.Pipe()
	require.NoError(t, err)
	os.Stdout, os.Stderr = w, w
	log.SetOutput(w)

	done := make(chan string)
	go func() {
		var buf bytes.Buffer
		_, _ = io.Copy(&buf, r)
		done <- buf.String()
	}()

	defer func() {
		os.Stdout, os.Stderr = origStdout, origStderr
		log.SetOutput(origLogOut)
	}()

	fn()

	require.NoError(t, w.Close())
	out := <-done
	require.NoError(t, r.Close())
	return out
}

// assertSecretNotLeaked fails if the secret, or any 8-character window of it,
// appears in out.
func assertSecretNotLeaked(t *testing.T, out string) {
	t.Helper()
	require.NotContains(t, out, testSPSecret, "full client secret leaked to output")
	for i := 0; i+8 <= len(testSPSecret); i++ {
		require.NotContains(t, out, testSPSecret[i:i+8], "partial client secret leaked to output")
	}
}

// stubAzureSPConstructors replaces the cloud-facing constructors with fakes so
// no test reaches Azure, restoring them on cleanup.
func stubAzureSPConstructors(t *testing.T, p azureSPProvisioner) {
	t.Helper()
	origTenant, origProv := resolveAzureTenantIDFn, newAzureSPProvisionerFn
	t.Cleanup(func() {
		resolveAzureTenantIDFn, newAzureSPProvisionerFn = origTenant, origProv
	})
	resolveAzureTenantIDFn = func(_ context.Context, _ string) (string, error) {
		return testTenantID, nil
	}
	newAzureSPProvisionerFn = func(_ string) (azureSPProvisioner, error) {
		return p, nil
	}
}

func newSecretTestProvisioner() *mockSPProvisioner {
	return &mockSPProvisioner{
		appObjectID: "obj-1",
		appID:       testSPAppID,
		secret:      testSPSecret,
		principalID: "principal-1",
		roleDefID:   "roledef-1",
	}
}

// TestAzureWizard_ClientSecretNeverPrinted drives the wizard's create-SP step,
// the credential collection step and the store step with a mocked Azure
// provisioner and a mock Secrets Manager. The secret must reach the store
// intact while never appearing on stdout, stderr or the logger.
func TestAzureWizard_ClientSecretNeverPrinted(t *testing.T) {
	prov := newSecretTestProvisioner()
	stubAzureSPConstructors(t, prov)

	origOpts := azureOpts
	t.Cleanup(func() { azureOpts = origOpts })
	azureOpts = AzureConfigOptions{StackName: "my-cudly"}

	store := NewMockSecretsStore()
	var created *AzureCredentials
	var stepErr, collectErr, storeErr error

	out := captureAllOutput(t, func() {
		created, stepErr = azureStepCreateServicePrincipal(context.Background(),
			bufio.NewReader(strings.NewReader("r\n")), testSubID)
		if stepErr != nil {
			return
		}
		// Empty reader: any prompt would fail, proving nothing is re-asked.
		var creds AzureCredentials
		creds, collectErr = collectAzureCredentials(bufio.NewReader(strings.NewReader("")), created)
		if collectErr != nil {
			return
		}
		storeErr = storeAzureCredentials(context.Background(), store, azureOpts.StackName, creds)
	})

	require.NoError(t, stepErr)
	require.NoError(t, collectErr)
	require.NoError(t, storeErr)
	require.NotNil(t, created)

	assertSecretNotLeaked(t, out)
	assert.Contains(t, out, testSPAppID, "non-secret appId should still be shown")
	assert.Contains(t, out, "[hidden]")
	assert.Contains(t, out, "my-cudly-AzureCredentials", "operator should be told where the secret is stored")

	stored, ok := store.updatedSecrets["my-cudly-AzureCredentials"]
	require.True(t, ok, "credentials must be written to Secrets Manager")
	var got AzureCredentials
	require.NoError(t, json.Unmarshal([]byte(stored), &got))
	assert.Equal(t, AzureCredentials{
		TenantID:       testTenantID,
		ClientID:       testSPAppID,
		ClientSecret:   testSPSecret,
		SubscriptionID: testSubID,
	}, got)
}

// TestPrintAzureSPResult_OmitsSecret pins the printer itself.
func TestPrintAzureSPResult_OmitsSecret(t *testing.T) {
	out := captureAllOutput(t, func() {
		printAzureSPResult(azureSPResult{
			AppID:        testSPAppID,
			ClientSecret: testSPSecret,
			TenantID:     testTenantID,
		}, testSubID, "stack")
	})
	assertSecretNotLeaked(t, out)
	assert.Contains(t, out, testSPAppID)
	assert.Contains(t, out, testTenantID)
	assert.Contains(t, out, testSubID)
}

// TestAzureWizard_SkipCreateSP_ReturnsNil checks that skipping Step 3 makes no
// Azure calls and leaves collection to the normal prompt/flag path.
func TestAzureWizard_SkipCreateSP_ReturnsNil(t *testing.T) {
	stubAzureSPConstructors(t, nil)
	newAzureSPProvisionerFn = func(_ string) (azureSPProvisioner, error) {
		t.Fatal("provisioner must not be built when the step is skipped")
		return nil, nil
	}

	var created *AzureCredentials
	var err error
	captureAllOutput(t, func() {
		created, err = azureStepCreateServicePrincipal(context.Background(),
			bufio.NewReader(strings.NewReader("s\n")), testSubID)
	})
	require.NoError(t, err)
	assert.Nil(t, created)
}

// TestAzureWizard_FailureAfterCreate_NoSecretInError covers the rollback path
// and the post-create recovery hint: neither error text nor output may carry
// the secret.
func TestAzureWizard_FailureAfterCreate_NoSecretInError(t *testing.T) {
	prov := newSecretTestProvisioner()
	prov.assignErr = errors.New("boom")
	stubAzureSPConstructors(t, prov)

	var err error
	out := captureAllOutput(t, func() {
		_, err = azureStepCreateServicePrincipal(context.Background(),
			bufio.NewReader(strings.NewReader("r\n")), testSubID)
	})
	require.Error(t, err)
	assertSecretNotLeaked(t, out+err.Error())

	created := &AzureCredentials{ClientID: testSPAppID, ClientSecret: testSPSecret}
	hinted := withAzureSPRecoveryHint(errors.New("store failed"), created)
	assertSecretNotLeaked(t, hinted.Error())
	assert.Contains(t, hinted.Error(), "az ad app credential reset --id "+testSPAppID)
	assert.ErrorContains(t, hinted, "store failed")

	plain := errors.New("plain")
	assert.Same(t, plain, withAzureSPRecoveryHint(plain, nil))
}
