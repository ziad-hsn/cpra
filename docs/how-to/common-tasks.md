# Common Tasks

This guide provides step-by-step instructions for common tasks you might perform with the CPRA monitoring system.

!!! note "Before you start"
    Complete the [Getting Started](../tutorials/getting-started.md) walkthrough so that the binary, monitors YAML, and mock servers are already running from the `cpra/` directory.

## System Overview

CPRA uses a three-pipeline architecture for processing monitors:

![Pipeline Flow Diagram](../images/pipeline-flow.png)

- **Pulse Pipeline**: Executes health checks
- **Intervention Pipeline**: Performs automated remediation
- **Code Pipeline**: Sends alert notifications

Each pipeline operates independently with its own queue and worker pool. For a detailed explanation of the architecture, see the [Architecture Overview](../explanation/architecture-overview.md) document.

---

## How to Create a Custom Monitor

**Goal**: Define a new monitor in a YAML file to check a service.

**Prerequisites**:
- A running CPRA application (see [Quick Start](../tutorials/quickstart.md)).
- An endpoint to monitor.

**Steps**:

1.  **Open your `monitors.yaml` file.**

2.  **Add a new monitor definition.** This example defines a monitor that checks an HTTP endpoint every 30 seconds.

    ```yaml
    - name: "my-service-health-check"
      pulse:
        type: "http"
        interval: "30s"
        timeout: "5s"
        http:
          url: "http://my-service.example.com/health"
          method: "GET"
          headers:
            - "Content-Type: application/json"
          expected_status: 200
    ```

3.  **Restart the CPRA application** to load the new monitor.

**Troubleshooting**:
- **Problem**: Monitor not loading.
  - **Solution**: Check the YAML syntax. Ensure all required fields are present. Check the application logs for parsing errors.

---

## How to Configure the Controller

**Goal**: Customize the controller's behavior, such as batch sizes and worker pool settings.

**Prerequisites**:
- A `main.go` file to initialize the controller.
- Familiarity with `controller.DefaultConfig()` structure (review the [API Reference](../reference/api-reference.md)).

**Steps**:

1.  **Get the default configuration.**
    ```go
    config := controller.DefaultConfig()
    ```

2.  **Modify the configuration values.** <!-- [IMPROVED] Fixed config field names to match actual struct -->
    ```go
    config.Debug = true
    config.BatchSize = 2000
    // Note: Worker pool settings apply to all queues (Pulse, Intervention, Code)
    config.WorkerConfig.MinWorkers = 10
    config.WorkerConfig.MaxWorkers = 100
    ```

3.  **Create the controller with the custom configuration.**
    ```go
    ctrl := controller.NewController(config)
    ```

**Troubleshooting**: <!-- [IMPROVED] Updated to reflect actual config structure -->
- **Problem**: Performance is not as expected.
  - **Solution**: Adjust the `WorkerConfig.MinWorkers` and `WorkerConfig.MaxWorkers` settings. Note that these settings apply to all three worker pools (Pulse, Intervention, Code) in the current implementation.

---

## How to Add a New System to the Controller

**Goal**: Extend the functionality of the monitoring system by adding a custom processing system.

**Prerequisites**:
- Familiarity with the ECS architecture and the `github.com/mlange-42/ark` library.
- A custom system that implements the `ark.System` interface.

**Steps**:

1.  **Define your custom system.**
    ```go
    type MyCustomSystem struct {
        // ... your system's fields
    }

    func (s *MyCustomSystem) Initialize(w *ecs.World) {
        // ... initialization logic
    }

    func (s *MyCustomSystem) Update(w *ecs.World) {
        // ... update logic, called on each tick
    }

    func (s *MyCustomSystem) Finalize(w *ecs.World) {
        // ... cleanup logic
    }
    ```

2.  **Add the system to the controller's world.** This requires modifying the `NewController` function or having a method to add systems. Assuming you have a way to access the world:

    ```go
    // In your main.go or a custom setup function
    ctrl := controller.NewController(controller.DefaultConfig())
    world := ctrl.GetWorld()

    mySystem := &MyCustomSystem{}
    world.AddSystem(mySystem)
    ```

**Troubleshooting**:
- **Problem**: System is not running.
  - **Solution**: Ensure your system is added to the world before the controller's `Start()` method is called. Check for any panics or errors during the system's `Initialize` or `Update` methods.

---

## How to Use the Queueing System for Custom Jobs

**Goal**: Leverage the built-in queueing and worker pool for your own custom background jobs.

**Prerequisites**:
- A running CPRA application.
- A custom job type that you want to process.

**Steps**:

1.  **Create a new queue.**
    ```go
    qConfig := queue.DefaultQueueConfig()
    qConfig.Name = "my-custom-queue"
    myQueue, err := queue.NewQueue(qConfig)
    if err != nil {
        log.Fatalf("Failed to create custom queue: %v", err)
    }
    ```

2.  **Create a worker pool for the queue.**
    ```go
    wpConfig := queue.DefaultWorkerPoolConfig()
    logger := log.New(os.Stdout, "", log.LstdFlags)
    myPool, err := queue.NewDynamicWorkerPool(myQueue, wpConfig, logger)
    if err != nil {
        log.Fatalf("Failed to create custom worker pool: %v", err)
    }
    myPool.Start()
    defer myPool.DrainAndStop()
    ```

3.  **Define and enqueue a job.**
    ```go
    type MyJob struct {
        Data string
    }

    func (j *MyJob) Execute() jobs.Result {
        // ... process the job
        fmt.Printf("Processing job with data: %s\n", j.Data)
        return jobs.Result{Success: true}
    }

    myQueue.Enqueue(&MyJob{Data: "some important data"})
    ```

**Troubleshooting**:
- **Problem**: Jobs are not being processed.
  - **Solution**: Make sure you have started the worker pool with `myPool.Start()`. Check that the queue is not full and that there are available workers.
