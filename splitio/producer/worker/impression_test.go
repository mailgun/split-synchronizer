package worker

import (
	"encoding/json"
	"errors"
	"io/ioutil"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/splitio/go-split-commons/v4/conf"
	"github.com/splitio/go-split-commons/v4/dtos"
	"github.com/splitio/go-split-commons/v4/provisional"
	"github.com/splitio/go-split-commons/v4/service/api"
	recorderMock "github.com/splitio/go-split-commons/v4/service/mocks"
	storageMock "github.com/splitio/go-split-commons/v4/storage/mocks"
	"github.com/splitio/go-split-commons/v4/telemetry"
	"github.com/splitio/go-toolkit/v5/logging"

	"github.com/splitio/split-synchronizer/v4/splitio/common/impressionlistener"
	ilMock "github.com/splitio/split-synchronizer/v4/splitio/common/impressionlistener/mocks"
	evCalcMock "github.com/splitio/split-synchronizer/v4/splitio/producer/evcalc/mocks"
)

func TestSynchronizeImpressionError(t *testing.T) {
	logger := logging.NewLogger(nil)

	impressionMockStorage := storageMock.MockImpressionStorage{
		CountCall: func() int64 { return 0 },
		PopNWithMetadataCall: func(n int64) ([]dtos.ImpressionQueueObject, error) {
			if n != 50 {
				t.Error("Wrong input parameter passed")
			}
			return make([]dtos.ImpressionQueueObject, 0), errors.New("Some")
		},
	}

	impressionMockRecorder := recorderMock.MockImpressionRecorder{}

	impressionSync, _ := NewImpressionRecordMultiple(
		impressionMockStorage,
		impressionMockRecorder,
		&ilMock.ImpressionBulkListenerMock{},
		&storageMock.MockTelemetryStorage{},
		logger,
		conf.ManagerConfig{ImpressionsMode: conf.ImpressionsModeDebug, OperationMode: conf.ProducerSync, ListenerEnabled: true},
		nil,
		&evCalcMock.EvCalcMock{
			StoreDataFlushedCall: func(_ time.Time, _ int, _ int64) { t.Error("StoreDataFlushedCall should not be called") },
			AcquireCall:          func() bool { t.Error("Aquire should not be called"); return false },
			ReleaseCall:          func() { t.Error("Release should not be called") },
			BusyCall:             func() bool { return false },
		},
	)

	err := impressionSync.SynchronizeImpressions(50)
	if err == nil {
		t.Error("It should return err")
	}
}

func TestSynhronizeImpressionWithNoImpressions(t *testing.T) {
	logger := logging.NewLogger(nil)
	impressionMockStorage := storageMock.MockImpressionStorage{
		CountCall: func() int64 { return 0 },
		PopNWithMetadataCall: func(n int64) ([]dtos.ImpressionQueueObject, error) {
			if n != 50 {
				t.Error("Wrong input parameter passed")
			}
			return make([]dtos.ImpressionQueueObject, 0), nil
		},
	}

	impressionMockRecorder := recorderMock.MockImpressionRecorder{
		RecordCall: func(impressions []dtos.ImpressionsDTO, metadata dtos.Metadata, extraHeaders map[string]string) error {
			t.Error("It should not be called")
			return nil
		},
	}

	impressionSync, _ := NewImpressionRecordMultiple(
		impressionMockStorage,
		impressionMockRecorder,
		&ilMock.ImpressionBulkListenerMock{},
		&storageMock.MockTelemetryStorage{},
		logger,
		conf.ManagerConfig{
			ImpressionsMode: conf.ImpressionsModeDebug,
			OperationMode:   conf.ProducerSync,
			ListenerEnabled: true,
		},
		nil,
		&evCalcMock.EvCalcMock{
			StoreDataFlushedCall: func(_ time.Time, _ int, _ int64) { t.Error("StoreDataFlushedCall should not be called") },
			AcquireCall:          func() bool { t.Error("Aquire should not be called"); return false },
			ReleaseCall:          func() { t.Error("Release should not be called") },
			BusyCall:             func() bool { return false },
		},
	)

	err := impressionSync.SynchronizeImpressions(50)
	if err != nil {
		t.Error("It should not return err")
	}
}

func wrapImpression(feature string) dtos.Impression {
	return dtos.Impression{
		BucketingKey: "someBucketingKey",
		ChangeNumber: 123456789,
		KeyName:      "someKey",
		Label:        "someLabel",
		Time:         time.Now().UTC().UnixNano() / int64(time.Millisecond),
		Treatment:    "someTreatment",
		FeatureName:  feature,
	}
}

func TestImpressionsSyncE2E(t *testing.T) {
	logger := logging.NewLogger(nil)
	var requestReceived int64

	metadata1 := dtos.Metadata{MachineIP: "1.1.1.1", MachineName: "machine1", SDKVersion: "go-1.1.1"}
	metadata2 := dtos.Metadata{MachineIP: "2.2.2.2", MachineName: "machine2", SDKVersion: "php-2.2.2"}

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/impressions" && r.Method != "POST" {
			t.Error("Invalid request. Should be POST to /impressions")
		}
		atomic.AddInt64(&requestReceived, 1)

		body, err := ioutil.ReadAll(r.Body)
		r.Body.Close()
		if err != nil {
			t.Error("Error reading body")
			return
		}

		var impressions []dtos.ImpressionsDTO
		err = json.Unmarshal(body, &impressions)
		if err != nil {
			t.Errorf("Error parsing json: %s", err)
			return
		}

		switch impressions[0].TestName {
		case "feature1":
			if r.Header.Get("SplitSDKVersion") != "go-1.1.1" {
				t.Error("Unexpected version in header")
			}
			if r.Header.Get("SplitSDKMachineName") != "machine1" {
				t.Error("Unexpected version in header")
			}
			if r.Header.Get("SplitSDKMachineIP") != "1.1.1.1" {
				t.Error("Unexpected version in header")
			}
			if len(impressions[0].KeyImpressions) != 3 {
				t.Error("Incorrect number of impressions")
			}
		case "feature2":
			if r.Header.Get("SplitSDKVersion") != "php-2.2.2" {
				t.Error("Unexpected version in header")
			}
			if r.Header.Get("SplitSDKMachineName") != "machine2" {
				t.Error("Unexpected version in header")
			}
			if r.Header.Get("SplitSDKMachineIP") != "2.2.2.2" {
				t.Error("Unexpected version in header")
			}
			if len(impressions[0].KeyImpressions) != 2 {
				t.Error("Incorrect number of impressions")
			}
		default:
			t.Error("Unexpected case")
		}
		return
	}))
	defer ts.Close()

	impressionRecorder := api.NewHTTPImpressionRecorder("", conf.AdvancedConfig{EventsURL: ts.URL, SdkURL: ts.URL}, logger)
	impressionMockStorage := storageMock.MockImpressionStorage{
		CountCall: func() int64 { return 0 },
		PopNWithMetadataCall: func(n int64) ([]dtos.ImpressionQueueObject, error) {
			if n != 50 {
				t.Error("Wrong input parameter passed")
			}
			return []dtos.ImpressionQueueObject{
				{Impression: wrapImpression("feature1"), Metadata: metadata1},
				{Impression: wrapImpression("feature2"), Metadata: metadata2},
				{Impression: wrapImpression("feature1"), Metadata: metadata1},
				{Impression: wrapImpression("feature2"), Metadata: metadata2},
				{Impression: wrapImpression("feature1"), Metadata: metadata1},
			}, nil
		},
	}

	impressionSync, _ := NewImpressionRecordMultiple(
		impressionMockStorage,
		impressionRecorder,
		&ilMock.ImpressionBulkListenerMock{
			SubmitCall: func(imps []impressionlistener.ImpressionsForListener, metadata *dtos.Metadata) error {
				if *metadata == metadata1 && len(imps[0].KeyImpressions) != 3 {
					t.Error("3 impressions should have been received for metadata 1. Got: ", len(imps[0].KeyImpressions))
				} else if *metadata == metadata2 && len(imps[0].KeyImpressions) != 2 {
					t.Error("3 impressions should have been received for metadata 2. Got: ", len(imps[0].KeyImpressions))
				}
				return nil
			},
		},
		&storageMock.MockTelemetryStorage{
			RecordSyncLatencyCall: func(resource int, latency time.Duration) {
				if resource != telemetry.ImpressionSync {
					t.Error("wrong resource")
				}
			},
			RecordSuccessfulSyncCall: func(resource int, when time.Time) {
				if resource != telemetry.ImpressionSync {
					t.Error("wrong resource")
				}
			},
			RecordImpressionsStatsCall: func(dataType int, count int64) {
				if dataType != telemetry.ImpressionsDeduped {
					t.Error("wrong datatype", dataType)
				}

				if count != 0 {
					t.Error("wrong count", count)
				}
			},
		},
		logger,
		conf.ManagerConfig{
			ImpressionsMode: conf.ImpressionsModeDebug,
			OperationMode:   conf.ProducerSync,
			ListenerEnabled: true,
		},
		nil,
		&evCalcMock.EvCalcMock{
			StoreDataFlushedCall: func(_ time.Time, _ int, _ int64) {},
			LambdaCall:           func() float64 { return 0 },
			AcquireCall:          func() bool { return true },
			ReleaseCall:          func() {},
			BusyCall:             func() bool { return false },
		},
	)

	impressionSync.SynchronizeImpressions(50)
	if requestReceived != 2 {
		t.Error("It should call twice")
	}
}

func TestSynhronizeImpressionSyncOptimized(t *testing.T) {
	logger := logging.NewLogger(nil)
	var requestReceived int64

	metadata1 := dtos.Metadata{MachineIP: "1.1.1.1", MachineName: "machine1", SDKVersion: "go-1.1.1"}
	metadata2 := dtos.Metadata{MachineIP: "2.2.2.2", MachineName: "machine2", SDKVersion: "php-2.2.2"}

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/impressions" && r.Method != "POST" {
			t.Error("Invalid request. Should be POST to /impressions")
		}
		atomic.AddInt64(&requestReceived, 1)

		body, err := ioutil.ReadAll(r.Body)
		r.Body.Close()
		if err != nil {
			t.Error("Error reading body")
			return
		}

		var impressions []dtos.ImpressionsDTO

		err = json.Unmarshal(body, &impressions)
		if err != nil {
			t.Errorf("Error parsing json: %s", err)
			return
		}

		switch impressions[0].TestName {
		case "feature1":
			if l := len(impressions[0].KeyImpressions); l != 1 {
				t.Error("Incorrect number of impressions:", l)
			}
		case "feature2":
			if len(impressions[0].KeyImpressions) != 1 {
				t.Error("Incorrect number of impressions")
			}
		default:
			t.Error("Unexpected case")
		}
		return
	}))
	defer ts.Close()

	impressionRecorder := api.NewHTTPImpressionRecorder("", conf.AdvancedConfig{EventsURL: ts.URL, SdkURL: ts.URL}, logger)

	impressionMockStorage := storageMock.MockImpressionStorage{
		CountCall: func() int64 { return 0 },
		PopNWithMetadataCall: func(n int64) ([]dtos.ImpressionQueueObject, error) {
			if n != 50 {
				t.Error("Wrong input parameter passed")
			}
			return []dtos.ImpressionQueueObject{
				{Impression: wrapImpression("feature1"), Metadata: metadata1},
				{Impression: wrapImpression("feature2"), Metadata: metadata2},
				{Impression: wrapImpression("feature1"), Metadata: metadata1},
				{Impression: wrapImpression("feature2"), Metadata: metadata2},
				{Impression: wrapImpression("feature1"), Metadata: metadata1},
			}, nil
		},
	}

	timesCalled := 0
	impressionSync, _ := NewImpressionRecordMultiple(
		impressionMockStorage,
		impressionRecorder,
		&ilMock.ImpressionBulkListenerMock{
			SubmitCall: func(imps []impressionlistener.ImpressionsForListener, metadata *dtos.Metadata) error {
				if *metadata == metadata1 && len(imps[0].KeyImpressions) != 3 {
					t.Error("3 impressions should have been received for metadata 1. Got: ", len(imps[0].KeyImpressions))
				} else if *metadata == metadata2 && len(imps[0].KeyImpressions) != 2 {
					t.Error("3 impressions should have been received for metadata 2. Got: ", len(imps[0].KeyImpressions))
				}
				return nil
			},
		},
		&storageMock.MockTelemetryStorage{
			RecordSyncLatencyCall: func(resource int, latency time.Duration) {
				if resource != telemetry.ImpressionSync {
					t.Error("wrong resource")
				}
			},
			RecordSuccessfulSyncCall: func(resource int, when time.Time) {
				if resource != telemetry.ImpressionSync {
					t.Error("wrong resource")
				}
			},
			RecordImpressionsStatsCall: func(dataType int, count int64) {
				timesCalled++
				expectedDeduped := int64(0)
				if timesCalled > 2 {
					expectedDeduped = 1
				}

				if dataType != telemetry.ImpressionsDeduped {
					t.Error("wrong datatype", dataType)
				}

				if count != expectedDeduped {
					t.Error("wrong count", count)
				}
			},
		},
		logger,
		conf.ManagerConfig{
			ImpressionsMode: conf.ImpressionsModeOptimized,
			OperationMode:   conf.ProducerSync,
			ListenerEnabled: true,
		},
		provisional.NewImpressionsCounter(),
		&evCalcMock.EvCalcMock{
			StoreDataFlushedCall: func(_ time.Time, _ int, _ int64) {},
			LambdaCall:           func() float64 { return 0 },
			AcquireCall:          func() bool { return true },
			ReleaseCall:          func() {},
			BusyCall:             func() bool { return false },
		},
	)

	impressionSync.SynchronizeImpressions(50)

	if requestReceived != 2 {
		t.Error("It should be called twice")
	}
}

func TestSynhronizeImpressionPt(t *testing.T) {
	logger := logging.NewLogger(nil)
	var requestReceived int64
	var pt int64

	metadata1 := dtos.Metadata{
		MachineIP:   "1.1.1.1",
		MachineName: "machine1",
		SDKVersion:  "go-1.1.1",
	}

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/impressions" && r.Method != "POST" {
			t.Error("Invalid request. Should be POST to /impressions")
		}
		atomic.AddInt64(&requestReceived, 1)

		body, err := ioutil.ReadAll(r.Body)
		r.Body.Close()
		if err != nil {
			t.Error("Error reading body")
			return
		}

		var impressions []dtos.ImpressionsDTO

		err = json.Unmarshal(body, &impressions)
		if err != nil {
			t.Errorf("Error parsing json: %s", err)
			return
		}

		if len(impressions[0].KeyImpressions) != 1 {
			t.Error("Incorrect number of impressions")
		}
		if atomic.LoadInt64(&requestReceived) == 1 {
			if impressions[0].KeyImpressions[0].Pt != 0 {
				t.Error("Unexpected pt")
			}
			atomic.StoreInt64(&pt, impressions[0].KeyImpressions[0].Time)
		}
		if atomic.LoadInt64(&requestReceived) == 2 {
			if impressions[0].KeyImpressions[0].Pt != atomic.LoadInt64(&pt) {
				t.Error("Unexpected pt")
			}
		}
		return
	}))
	defer ts.Close()

	impressionRecorder := api.NewHTTPImpressionRecorder(
		"",
		conf.AdvancedConfig{
			EventsURL: ts.URL,
			SdkURL:    ts.URL,
		},
		logger,
	)

	impressionMockStorage := storageMock.MockImpressionStorage{
		CountCall: func() int64 { return 0 },
		PopNWithMetadataCall: func(n int64) ([]dtos.ImpressionQueueObject, error) {
			if n != 50 {
				t.Error("Wrong input parameter passed")
			}
			return []dtos.ImpressionQueueObject{
				{Impression: wrapImpression("feature1"), Metadata: metadata1},
			}, nil
		},
	}

	impressionSync, _ := NewImpressionRecordMultiple(
		impressionMockStorage,
		impressionRecorder,
		&ilMock.ImpressionBulkListenerMock{
			SubmitCall: func(imps []impressionlistener.ImpressionsForListener, metadata *dtos.Metadata) error {
				if *metadata == metadata1 && len(imps[0].KeyImpressions) != 1 {
					t.Error("3 impressions should have been received for metadata 1. Got: ", len(imps[0].KeyImpressions))
				}
				return nil
			},
		},
		&storageMock.MockTelemetryStorage{
			RecordSyncLatencyCall: func(resource int, latency time.Duration) {
				if resource != telemetry.ImpressionSync {
					t.Error("wrong resource")
				}
			},
			RecordSuccessfulSyncCall: func(resource int, when time.Time) {
				if resource != telemetry.ImpressionSync {
					t.Error("wrong resource")
				}
			},
			RecordImpressionsStatsCall: func(dataType int, count int64) {
				if dataType != telemetry.ImpressionsDeduped {
					t.Error("wrong datatype")
				}

				if count != 0 {
					t.Error("wrong count", count)
				}
			},
		},
		logger,
		conf.ManagerConfig{
			ImpressionsMode: conf.ImpressionsModeDebug,
			OperationMode:   conf.ProducerSync,
			ListenerEnabled: true,
		},
		nil,
		&evCalcMock.EvCalcMock{
			StoreDataFlushedCall: func(_ time.Time, _ int, _ int64) {},
			LambdaCall:           func() float64 { return 0 },
			AcquireCall:          func() bool { return true },
			ReleaseCall:          func() {},
			BusyCall:             func() bool { return false },
		},
	)

	impressionSync.SynchronizeImpressions(50)
	impressionSync.SynchronizeImpressions(50)

	if requestReceived != 2 {
		t.Error("It should call twice")
	}
}
