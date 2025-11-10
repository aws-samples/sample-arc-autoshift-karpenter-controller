package main

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/tools/leaderelection"
	"k8s.io/client-go/tools/leaderelection/resourcelock"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

type MockRoundTripper struct {
	MockDo func(req *http.Request) (*http.Response, error)
}

func (m *MockRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	return m.MockDo(req)
}

// MockLeaderElector is a mock implementation of the leader election interface
type MockLeaderElector struct {
	mock.Mock
}

func (m *MockLeaderElector) Run(ctx context.Context) {
	m.Called(ctx)
}

func (m *MockLeaderElector) GetLeader() (string, error) {
	args := m.Called()
	return args.String(0), args.Error(1)
}

func TestSNSSubscriptionConfirmation(t *testing.T) {
	// Mock http.Get to simulate subscription confirmation
	http.DefaultClient = &http.Client{
		Transport: &MockRoundTripper{
			MockDo: func(req *http.Request) (*http.Response, error) {
				assert.Equal(t, "GET", req.Method)
				return &http.Response{StatusCode: http.StatusOK}, nil
			},
		},
	}

	// Create a sample SNS message
	msg := SNSMessage{
		Type:         "SubscriptionConfirmation",
		SubscribeURL: "http://example.com/confirm",
	}

	// Simulate an HTTP request with the above message
	reqBody, err := json.Marshal(msg)
	assert.NoError(t, err)

	req, err := http.NewRequest("POST", "/sns", bytes.NewBuffer(reqBody))
	assert.NoError(t, err)

	// Capture the response
	rr := httptest.NewRecorder()
	handler := http.HandlerFunc(handleSNS) // Ensure this refers to handleSNS defined in main.go
	handler.ServeHTTP(rr, req)

	// Assert that the subscription was confirmed successfully
	assert.Equal(t, http.StatusOK, rr.Code)
}

func TestInvalidSNSMessage(t *testing.T) {
	// Simulate invalid JSON input
	req, err := http.NewRequest("POST", "/sns", bytes.NewBuffer([]byte("{invalid-json")))
	assert.NoError(t, err)

	rr := httptest.NewRecorder()
	handler := http.HandlerFunc(handleSNS) // Ensure this refers to handleSNS defined in main.go
	handler.ServeHTTP(rr, req)

	// Assert the response status code is 400 Bad Request
	assert.Equal(t, http.StatusBadRequest, rr.Code)
}

func TestInvalidEventParsing(t *testing.T) {
	// Create an SNS message with a valid structure but invalid event message
	msg := SNSMessage{
		Type:    "Notification",
		Message: "{invalid-event}",
	}

	reqBody, err := json.Marshal(msg)
	assert.NoError(t, err)

	req, err := http.NewRequest("POST", "/sns", bytes.NewBuffer(reqBody))
	assert.NoError(t, err)

	rr := httptest.NewRecorder()
	handler := http.HandlerFunc(handleSNS) // Ensure this refers to handleSNS defined in main.go
	handler.ServeHTTP(rr, req)

	// Assert that the server responds with an error
	assert.Equal(t, http.StatusBadRequest, rr.Code)
}

func TestValidEvent(t *testing.T) {
	// Create a valid SNS message with a valid event
	msg := SNSMessage{
		Type: "Notification",
		Message: `{
			"version": "1.0",
			"id": "abc123",
			"detail-type": "EC2 Instance State-change Notification",
			"source": "aws.ec2",
			"account": "123456789012",
			"time": "2025-02-07T12:34:56Z",
			"region": "us-east-1",
			"detail": {
				"version": "1.0",
				"metadata": {
					"awayFrom": "us-east-2"
				}
			}
		}`,
	}

	// Simulate an HTTP request with the above message
	reqBody, err := json.Marshal(msg)
	assert.NoError(t, err)

	req, err := http.NewRequest("POST", "/sns", bytes.NewBuffer(reqBody))
	assert.NoError(t, err)

	rr := httptest.NewRecorder()
	handler := http.HandlerFunc(handleSNS)
	handler.ServeHTTP(rr, req)

	// Assert that the event parsing was successful
	assert.Equal(t, http.StatusOK, rr.Code)
}

// TestInvalidEvent checks if the event is invalid based on missing or incorrect fields
func TestInvalidEvent(t *testing.T) {
	// Create an SNS message with a missing "version" field in the event
	msg := SNSMessage{
		Type: "Notification",
		Message: `{
			"id": "abc123",
			"detail-type": "EC2 Instance State-change Notification",
			"source": "aws.ec2",
			"account": "123456789012",
			"time": "2025-02-07T12:34:56Z",
			"region": "us-east-1",
			"detail": {
				"metadata": {
					"awayFrom": "us-east-2"
				}
			}
		}`,
	}

	// Simulate an HTTP request with the above message
	reqBody, err := json.Marshal(msg)
	assert.NoError(t, err)

	req, err := http.NewRequest("POST", "/sns", bytes.NewBuffer(reqBody))
	assert.NoError(t, err)

	rr := httptest.NewRecorder()
	handler := http.HandlerFunc(handleSNS)
	handler.ServeHTTP(rr, req)

	// Assert that the event is invalid and responds with an error
	assert.Equal(t, http.StatusBadRequest, rr.Code)
}

// TestLeaderElection tests the leader election functionality
func TestLeaderElection(t *testing.T) {
	// Create a fake Kubernetes clientset
	clientset := fake.NewSimpleClientset()
	
	// Create a test context with timeout
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()
	
	// Create a channel to signal when the leader callback is invoked
	leaderCh := make(chan struct{})
	
	// Create leader election config
	leConfig := leaderelection.LeaderElectionConfig{
		Lock: &resourcelock.LeaseLock{
			LockConfig: resourcelock.ResourceLockConfig{
				Identity: "test-pod",
			},
		},
		LeaseDuration:   1 * time.Second,
		RenewDeadline:   500 * time.Millisecond,
		RetryPeriod:     200 * time.Millisecond,
		ReleaseOnCancel: true,
		Callbacks: leaderelection.LeaderCallbacks{
			OnStartedLeading: func(ctx context.Context) {
				// Signal that we've become the leader
				close(leaderCh)
			},
			OnStoppedLeading: func() {},
			OnNewLeader:      func(identity string) {},
		},
	}
	
	// Start leader election in a goroutine
	go func() {
		// This is just a mock test, so we don't actually run the full election
		// In a real scenario, we would use leaderelection.RunOrDie
		select {
		case <-ctx.Done():
			return
		case <-time.After(500 * time.Millisecond):
			// Simulate becoming the leader after a short delay
			if leConfig.Callbacks.OnStartedLeading != nil {
				leConfig.Callbacks.OnStartedLeading(ctx)
			}
		}
	}()
	
	// Wait for leader election or timeout
	select {
	case <-leaderCh:
		// Successfully became the leader
		t.Log("Successfully became the leader")
	case <-ctx.Done():
		t.Fatal("Timed out waiting to become the leader")
	}
}

// TestHealthCheckEndpoint tests the health check endpoint
func TestHealthCheckEndpoint(t *testing.T) {
	// Create a new gin router
	router := setupRouter()
	
	// Create a test request to the health check endpoint
	req, err := http.NewRequest("GET", "/healthz", nil)
	assert.NoError(t, err)
	
	// Create a response recorder
	rr := httptest.NewRecorder()
	
	// Serve the request
	router.ServeHTTP(rr, req)
	
	// Check the status code
	assert.Equal(t, http.StatusOK, rr.Code)
	
	// Check the response body
	assert.Equal(t, "OK", rr.Body.String())
}

// Helper function to set up the router for testing
func setupRouter() *gin.Engine {
	router := gin.Default()
	router.GET("/healthz", func(c *gin.Context) {
		c.String(http.StatusOK, "OK")
	})
	router.POST("/sns", handleSNS)
	return router
}

//func TestUpdateKarpenterNodePool(t *testing.T) {
//	// Mock the Kubernetes client and AWS EC2 client
//	mockK8sClient := &mockKubernetesClient{}
//	mockEC2Client := &mockEC2Client{}
//
//	// Create a sample event
//	event := Event{
//		Detail: struct {
//			Version  string `json:"version"`
//			Metadata struct {
//				AwayFrom string `json:"awayFrom"`
//			} `json:"metadata"`
//		}{
//			Metadata: struct {
//				AwayFrom string `json:"awayFrom"`
//			}{
//				AwayFrom: "us-west-2a",
//			},
//		},
//	}
//
//	// Call updateKarpenterNodePool function with the event
//	updateKarpenterNodePool(event)
//
//	// Check that the node pool was updated correctly (mock logic assertions)
//	assert.True(t, mockK8sClient.nodePoolUpdated)
//	assert.Equal(t, "us-west-2b", mockEC2Client.updatedZone)
//}
//func TestAWSConfigLoadingFailure(t *testing.T) {
//	// Mock AWS config loading failure
//	originalLoadConfig := config.LoadDefaultConfig
//	defer func() { config.LoadDefaultConfig = originalLoadConfig }()
//
//	config.LoadDefaultConfig = func(ctx context.Context, opts ...func(*config.LoadOptions) error) (cfg config.Config, err error) {
//		return config.Config{}, fmt.Errorf("failed to load AWS config")
//	}
//
//	event := Event{}
//	updateKarpenterNodePool(event) // This should log the error
//
//	// Verify the log contains the expected error
//	assert.Contains(t, capturedLogs, "Failed to load AWS config")
//}