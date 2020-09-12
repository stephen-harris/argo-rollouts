package datadog

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/argoproj/argo-rollouts/pkg/apis/rollouts/v1alpha1"
	log "github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
)

func TestRunSuite(t *testing.T) {

	const expectedApiKey = "a63676c75786a66c3832753378667878"
	const expectedAppKey = "71747a306331793561613434626c6b6538677377"

	unixNow = func() int64 { return 1599076435 }

	// Test Cases
	var tests = []struct {
		webServerStatus         int
		webServerResponse       string
		metric                  v1alpha1.Metric
		expectedIntervalSeconds int64
		expectedValue           string
		expectedPhase           v1alpha1.AnalysisPhase
		expectedErrorMessage    string
	}{
		// When last value of time series matches condition then succeed.
		{
			webServerStatus:   200,
			webServerResponse: `{"status":"ok","series":[{"pointlist":[[1598867910000,0.0020008318672513122],[1598867925000,0.0003332881882246533]]}]}`,
			metric: v1alpha1.Metric{
				Name:             "foo",
				SuccessCondition: "asFloat(result) < 0.001",
				FailureCondition: "asFloat(result) >= 0.001",
				Provider: v1alpha1.MetricProvider{
					Datadog: &v1alpha1.DatadogMetric{
						Query:    "avg:kubernetes.cpu.user.total{*}",
						Interval: "10m",
						APIKey:   expectedApiKey,
						APPKey:   expectedAppKey,
					},
				},
			},
			expectedIntervalSeconds: 600,
			expectedValue:           "0.0003332881882246533",
			expectedPhase:           v1alpha1.AnalysisPhaseSuccessful,
		},
		// When last value of time series does not match condition then fail.
		{
			webServerStatus:   200,
			webServerResponse: `{"status":"ok","series":[{"pointlist":[[1598867910000,0.0020008318672513122],[1598867925000,0.006121378742186943]]}]}`,
			metric: v1alpha1.Metric{
				Name:             "foo",
				SuccessCondition: "asFloat(result) < 0.001",
				FailureCondition: "asFloat(result) >= 0.001",
				Provider: v1alpha1.MetricProvider{
					Datadog: &v1alpha1.DatadogMetric{
						Query:  "avg:kubernetes.cpu.user.total{*}",
						APIKey: expectedApiKey,
						APPKey: expectedAppKey,
					},
				},
			},
			expectedIntervalSeconds: 300,
			expectedValue:           "0.006121378742186943",
			expectedPhase:           v1alpha1.AnalysisPhaseFailed,
		},
		// Error if the request is invalid
		{
			webServerStatus:   400,
			webServerResponse: `{"status":"error","error":"error messsage"}`,
			metric: v1alpha1.Metric{
				Name:             "foo",
				SuccessCondition: "asFloat(result) < 0.001",
				FailureCondition: "asFloat(result) >= 0.001",
				Provider: v1alpha1.MetricProvider{
					Datadog: &v1alpha1.DatadogMetric{
						Query:  "avg:kubernetes.cpu.user.total{*}",
						APIKey: expectedApiKey,
						APPKey: expectedAppKey,
					},
				},
			},
			expectedIntervalSeconds: 300,
			expectedPhase:           v1alpha1.AnalysisPhaseError,
			expectedErrorMessage:    "received non 2xx response code: 400 {\"status\":\"error\",\"error\":\"error messsage\"}",
		},
		// Error if there is an authentication issue
		{
			webServerStatus:   401,
			webServerResponse: `{"errors": ["No authenticated user."]}`,
			metric: v1alpha1.Metric{
				Name:             "foo",
				SuccessCondition: "asFloat(result) < 0.001",
				FailureCondition: "asFloat(result) >= 0.001",
				Provider: v1alpha1.MetricProvider{
					Datadog: &v1alpha1.DatadogMetric{
						Query:  "avg:kubernetes.cpu.user.total{*}",
						APIKey: expectedApiKey,
						APPKey: expectedAppKey,
					},
				},
			},
			expectedIntervalSeconds: 300,
			expectedPhase:           v1alpha1.AnalysisPhaseError,
			expectedErrorMessage:    "received authentication error response code: 401 {\"errors\": [\"No authenticated user.\"]}",
		},
	}

	// Run

	for _, test := range tests {
		// Server setup with response
		server := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {

			//Check query variables
			actualQuery := req.URL.Query().Get("query")
			actualFrom := req.URL.Query().Get("from")
			actualTo := req.URL.Query().Get("to")

			if actualQuery != "avg:kubernetes.cpu.user.total{*}" {
				t.Errorf("\nquery expected avg:kubernetes.cpu.user.total{*} but got %s", actualQuery)
			}

			if from, err := strconv.ParseInt(actualFrom, 10, 64); err == nil && from != unixNow()-test.expectedIntervalSeconds {
				t.Errorf("\nfrom %d expected be equal to %d", from, unixNow()-test.expectedIntervalSeconds)
			} else if err != nil {
				t.Errorf("\nfailed to parse from: %v", err)
			}

			if to, err := strconv.ParseInt(actualTo, 10, 64); err == nil && to != unixNow() {
				t.Errorf("\nto %d was expected be equal to %d", to, unixNow())
			} else if err != nil {
				t.Errorf("\nfailed to parse to: %v", err)
			}

			//Check headers
			if req.Header.Get("Content-Type") != "application/json" {
				t.Errorf("\nContent-Type header expected to be application/json but got %s", req.Header.Get("Content-Type"))
			}
			if req.Header.Get("DD-API-KEY") != expectedApiKey {
				t.Errorf("\nDD-API-KEY header expected %s but got %s", expectedApiKey, req.Header.Get("DD-API-KEY"))
			}
			if req.Header.Get("DD-APPLICATION-KEY") != expectedAppKey {
				t.Errorf("\nDD-APPLICATION-KEY header expected %s but got %s", expectedAppKey, req.Header.Get("DD-APPLICATION-KEY"))
			}

			// Return mock response
			if test.webServerStatus < 200 || test.webServerStatus >= 300 {
				http.Error(rw, test.webServerResponse, test.webServerStatus)
			} else {
				rw.Header().Set("Content-Type", "application/json")
				io.WriteString(rw, test.webServerResponse)
			}
		}))
		defer server.Close()

		test.metric.Provider.Datadog.Address = server.URL

		logCtx := log.WithField("test", "test")

		provider := NewDatadogProvider(*logCtx)

		// Get our result
		measurement := provider.Run(newAnalysisRun(), test.metric)

		// Common Asserts
		assert.NotNil(t, measurement)
		assert.Equal(t, string(test.expectedPhase), string(measurement.Phase))

		// Phase specific cases
		switch test.expectedPhase {
		case v1alpha1.AnalysisPhaseSuccessful:
			assert.NotNil(t, measurement.StartedAt)
			assert.Equal(t, test.expectedValue, measurement.Value)
			assert.NotNil(t, measurement.FinishedAt)
		case v1alpha1.AnalysisPhaseFailed:
			assert.NotNil(t, measurement.StartedAt)
			assert.Equal(t, test.expectedValue, measurement.Value)
			assert.NotNil(t, measurement.FinishedAt)
		case v1alpha1.AnalysisPhaseError:
			assert.Contains(t, measurement.Message, test.expectedErrorMessage)
		}

	}
}

func newAnalysisRun() *v1alpha1.AnalysisRun {
	return &v1alpha1.AnalysisRun{}
}
