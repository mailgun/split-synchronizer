package proxy

import (
	"fmt"
	"os"
	"strconv"
	"sync"
	"time"

	cfg "github.com/splitio/go-split-commons/v4/conf"
	"github.com/splitio/go-split-commons/v4/service/api"
	"github.com/splitio/go-split-commons/v4/synchronizer"
	"github.com/splitio/go-split-commons/v4/telemetry"

	// 	"github.com/splitio/go-split-commons/v4/synchronizer/worker/metric"
	"github.com/splitio/go-split-commons/v4/tasks"
	"github.com/splitio/split-synchronizer/v4/conf"
	"github.com/splitio/split-synchronizer/v4/log"
	"github.com/splitio/split-synchronizer/v4/splitio"
	"github.com/splitio/split-synchronizer/v4/splitio/common"
	proxyTelemetry "github.com/splitio/split-synchronizer/v4/splitio/common/telemetry"
	"github.com/splitio/split-synchronizer/v4/splitio/proxy/boltdb"
	"github.com/splitio/split-synchronizer/v4/splitio/proxy/boltdb/collections"
	"github.com/splitio/split-synchronizer/v4/splitio/proxy/controllers"
	"github.com/splitio/split-synchronizer/v4/splitio/proxy/fetcher"
	"github.com/splitio/split-synchronizer/v4/splitio/proxy/interfaces"
	"github.com/splitio/split-synchronizer/v4/splitio/proxy/storage"
	"github.com/splitio/split-synchronizer/v4/splitio/recorder"
	"github.com/splitio/split-synchronizer/v4/splitio/task"
	"github.com/splitio/split-synchronizer/v4/splitio/util"
)

func gracefulShutdownProxy(sigs chan os.Signal, gracefulShutdownWaitingGroup *sync.WaitGroup, syncManager synchronizer.Manager) {
	<-sigs

	log.PostShutdownMessageToSlack(false)

	fmt.Println("\n\n * Starting graceful shutdown")
	fmt.Println("")

	// Events - Emit task stop signal
	fmt.Println(" -> Sending STOP to impression posting goroutine")
	controllers.StopEventsRecording()

	// Impressions - Emit task stop signal
	fmt.Println(" -> Sending STOP to event posting goroutine")
	controllers.StopImpressionsRecording()

	// Healthcheck - Emit task stop signal
	fmt.Println(" -> Sending STOP to healthcheck goroutine")
	task.StopHealtcheck()

	// Stopping Sync Manager in charge of PeriodicFetchers and PeriodicRecorders as well as Streaming
	fmt.Println(" -> Sending STOP to Synchronizer")
	syncManager.Stop()

	fmt.Println(" * Waiting goroutines stop")
	gracefulShutdownWaitingGroup.Wait()

	fmt.Println(" * Shutting it down - see you soon!")
	os.Exit(splitio.SuccessfulOperation)
}

// Start initialize in proxy mode
func Start(sigs chan os.Signal, gracefulShutdownWaitingGroup *sync.WaitGroup) {

	clientKey, err := util.GetClientKey(conf.Data.APIKey)
	if err != nil {
		log.Instance.Error(err)
		os.Exit(1) // TODO(mredolatti): set an appropriate exitcode here
	}

	// Initialization of DB
	var dbpath = boltdb.InMemoryMode
	if conf.Data.Proxy.PersistMemoryPath != "" {
		dbpath = conf.Data.Proxy.PersistMemoryPath
	}
	boltdb.Initialize(dbpath, nil)

	// Getting initial config data
	advanced := conf.ParseAdvancedOptions()
	metadata := util.GetMetadata()

	// Initialization common
	interfaces.Initialize()

	// Setup fetchers & recorders
	splitAPI := api.NewSplitAPI(conf.Data.APIKey, advanced, log.Instance, metadata)

	// Instantiating storages
	splitCollection := collections.NewSplitChangesCollection(boltdb.DBB)
	splitStorage := storage.NewSplitStorage(splitCollection)
	segmentCollection := collections.NewSegmentChangesCollection(boltdb.DBB)
	segmentStorage := storage.NewSegmentStorage(segmentCollection)

	telemetryRecorder := api.NewHTTPTelemetryRecorder(conf.Data.APIKey, advanced, log.Instance)
	localTelemetryStorage := proxyTelemetry.NewProxyTelemetryFacade()

	// Creating Workers and Tasks
	workers := synchronizer.Workers{
		SplitFetcher:   fetcher.NewSplitFetcher(splitCollection, splitAPI.SplitFetcher, localTelemetryStorage, log.Instance),
		SegmentFetcher: fetcher.NewSegmentFetcher(segmentCollection, splitCollection, splitAPI.SegmentFetcher, localTelemetryStorage, log.Instance),
		TelemetryRecorder: telemetry.NewTelemetrySynchronizer(localTelemetryStorage, telemetryRecorder, splitStorage, segmentStorage, log.Instance,
			metadata, localTelemetryStorage),
	}

	stasks := synchronizer.SplitTasks{
		SplitSyncTask: tasks.NewFetchSplitsTask(workers.SplitFetcher, conf.Data.SplitsFetchRate, log.Instance),
		SegmentSyncTask: tasks.NewFetchSegmentsTask(workers.SegmentFetcher, conf.Data.SegmentFetchRate, advanced.SegmentWorkers,
			advanced.SegmentQueueSize, log.Instance),
		TelemetrySyncTask: tasks.NewRecordTelemetryTask(workers.TelemetryRecorder, conf.Data.MetricsPostRate, log.Instance),
	}

	// Creating Synchronizer for tasks
	syncImpl := synchronizer.NewSynchronizer(advanced, stasks, workers, log.Instance, nil)

	managerStatus := make(chan int, 1)
	syncManager, err := synchronizer.NewSynchronizerManager(
		syncImpl,
		log.Instance,
		advanced,
		splitAPI.AuthClient,
		splitStorage,
		managerStatus,
		localTelemetryStorage,
		metadata,
		&clientKey,
	)
	if err != nil {
		panic(err)
	}

	// Proxy mode - graceful shutdown
	go gracefulShutdownProxy(sigs, gracefulShutdownWaitingGroup, syncManager)

	// Run Sync Manager
	before := time.Now()
	go syncManager.Start()
	select {
	case status := <-managerStatus:
		switch status {
		case synchronizer.Ready:
			log.Instance.Info("Synchronizer tasks started")
			workers.TelemetryRecorder.SynchronizeConfig(
				telemetry.InitConfig{
					AdvancedConfig: advanced,
					TaskPeriods: cfg.TaskPeriods{
						SplitSync:      conf.Data.SplitsFetchRate,
						SegmentSync:    conf.Data.SegmentFetchRate,
						ImpressionSync: conf.Data.ImpressionsPostRate,
						TelemetrySync:  10, // TODO(mredolatti): Expose this as a config option
					},
					ManagerConfig: cfg.ManagerConfig{
						ImpressionsMode: conf.Data.ImpressionsMode,
						ListenerEnabled: conf.Data.ImpressionListener.Endpoint != "",
					},
				},
				time.Now().Sub(before).Milliseconds(),
				map[string]int64{conf.Data.APIKey: 1},
				nil,
			)
		case synchronizer.Error:
			log.Instance.Error("Initial synchronization failed. Either split is unreachable or the APIKey is incorrect. Aborting execution.")
			os.Exit(splitio.ExitTaskInitialization)
		}
	}

	if conf.Data.ImpressionListener.Endpoint != "" {
		go task.PostImpressionsToListener(recorder.ImpressionListenerSubmitter{
			Endpoint: conf.Data.ImpressionListener.Endpoint,
		})
	}

	// Initialization routes
	controllers.InitializeImpressionWorkers(conf.Data.Proxy.ImpressionsMaxSize, int64(conf.Data.ImpressionsPostRate), gracefulShutdownWaitingGroup)
	controllers.InitializeEventWorkers(conf.Data.Proxy.EventsMaxSize, int64(conf.Data.EventsPostRate), gracefulShutdownWaitingGroup)
	controllers.InitializeImpressionsCountRecorder()
	controllers.InitializeTelemetryRecorder()

	httpClients := common.HTTPClients{
		SdkClient:    api.NewHTTPClient(conf.Data.APIKey, advanced, advanced.SdkURL, log.Instance, metadata),
		EventsClient: api.NewHTTPClient(conf.Data.APIKey, advanced, advanced.EventsURL, log.Instance, metadata),
		AuthClient:   api.NewHTTPClient(conf.Data.APIKey, advanced, advanced.AuthServiceURL, log.Instance, metadata),
	}

	proxyOptions := &Options{
		Port:                      ":" + strconv.Itoa(conf.Data.Proxy.Port),
		APIKeys:                   conf.Data.Proxy.Auth.APIKeys,
		AdminPort:                 conf.Data.Proxy.AdminPort,
		AdminUsername:             conf.Data.Proxy.AdminUsername,
		AdminPassword:             conf.Data.Proxy.AdminPassword,
		DebugOn:                   conf.Data.Logger.DebugOn,
		ImpressionListenerEnabled: conf.Data.ImpressionListener.Endpoint != "",
		httpClients:               httpClients,
		splitStorage:              splitStorage,
		segmentStorage:            segmentStorage,
		latencyStorage:            localTelemetryStorage,
	}

	go task.CheckEnvirontmentStatus(gracefulShutdownWaitingGroup, splitStorage, httpClients)

	// Run webserver loop
	Run(proxyOptions)
}
