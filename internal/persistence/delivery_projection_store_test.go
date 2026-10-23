package persistence

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"testing"
	"time"
)

const notificationProjectionCrashMarker = "notification projection page committed"

// The child writes real Raft logs and optionally a snapshot, then waits after
// one committed page. The parent kills it without Store.Close. No provider,
// controller or scheduler is constructed by either process.
func TestNotificationProjectionProcessRestart(t *testing.T) {
	if directory := os.Getenv("CPRA_TEST_NOTIFICATION_PROJECTION_DIR"); directory != "" {
		config := testConfig(t)
		config.Storage.Directory = directory
		format, err := strconv.Atoi(os.Getenv("CPRA_TEST_NOTIFICATION_PROJECTION_FORMAT"))
		if err != nil {
			t.Fatal(err)
		}
		store, err := openReplayStore(t, config)
		if err != nil {
			t.Fatal(err)
		}
		_, m, _, _ := archivedNotificationFixtureOn(t, store, format, 70, Started)
		command := Command{Kind: "action_project", Revision: "notifications", MonitorID: m.ID,
			Generation: store.fsm.image.ActionProjectionState.Revision, At: time.Now().UTC()}
		result := submit(t, store, command)[0]
		if result.Err != nil || !result.Allowed || store.fsm.image.ActionProjectionState.Notifications == nil {
			t.Fatal("fixture did not stop at a committed partial page", result.Err)
		}
		if os.Getenv("CPRA_TEST_NOTIFICATION_PROJECTION_SNAPSHOT") == "1" {
			persistPausedReviewSnapshot(t, store)
		}
		fmt.Println(notificationProjectionCrashMarker)
		select {}
	}
	formats := []int{ActionFormatVersion}
	if externalStorageFormat(45) {
		formats = append(formats, 45)
	}
	for _, format := range formats {
		for _, snapshot := range []bool{false, true} {
			t.Run(fmt.Sprintf("format%d/snapshot-%t", format, snapshot), func(t *testing.T) {
				config := testConfig(t)
				executable, err := os.Executable()
				if err != nil {
					t.Fatal(err)
				}
				ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
				defer cancel()
				command := exec.CommandContext(ctx, executable, "-test.run=^TestNotificationProjectionProcessRestart$")
				command.Env = append(os.Environ(), "CPRA_TEST_NOTIFICATION_PROJECTION_DIR="+config.Storage.Directory,
					"CPRA_TEST_NOTIFICATION_PROJECTION_FORMAT="+strconv.Itoa(format),
					"CPRA_TEST_NOTIFICATION_PROJECTION_SNAPSHOT="+strconv.Itoa(map[bool]int{false: 0, true: 1}[snapshot]))
				stdout, err := command.StdoutPipe()
				if err != nil {
					t.Fatal(err)
				}
				var stderr bytes.Buffer
				command.Stderr = &stderr
				if err := command.Start(); err != nil {
					t.Fatal(err)
				}
				defer func() { _ = command.Process.Kill() }()
				ready := make(chan bool, 1)
				go func() {
					scanner := bufio.NewScanner(stdout)
					for scanner.Scan() {
						if scanner.Text() == notificationProjectionCrashMarker {
							ready <- true
							return
						}
					}
					ready <- false
				}()
				select {
				case success := <-ready:
					if !success {
						_ = command.Wait()
						t.Fatalf("fixture stopped before its committed page: %s", stderr.String())
					}
				case <-ctx.Done():
					_ = command.Wait()
					t.Fatal("fixture did not commit its bounded page before deadline")
				}
				if err := command.Process.Kill(); err != nil {
					t.Fatal(err)
				}
				_ = command.Wait()
				// Inspect only replayed committed state before opening-time
				// conversion, proving the durable cursor and its exact fence.
				paused, err := openReplayStore(t, config)
				if err != nil {
					t.Fatal(err)
				}
				state := paused.fsm.image.ActionProjectionState.clone()
				if state == nil || state.Notifications == nil || state.Notifications.After == "" || state.Notifications.Fence != paused.fsm.image.ActionStorage.Sequence {
					t.Fatal("crash recovery lost the committed migration fence/cursor")
				}
				m, _ := paused.Get("stable-one")
				m.Actions = make(map[string]Action)
				pin, err := paused.fsm.collections.pinActions()
				if err != nil {
					t.Fatal(err)
				}
				rows, next, err := pin.Page(t.Context(), m.ID, "", 100)
				pin.Close()
				if err != nil || next != "" {
					t.Fatal("unexpected bounded paused fixture inventory", err)
				}
				for _, row := range rows {
					m.Actions[row.Action.ID] = row.Action
				}
				opening := notificationByColor(t, m, "red", Succeeded)
				closing := notificationByColor(t, m, "green", Started)
				if err := paused.Close(); err != nil {
					t.Fatal(err)
				}
				store, err := Open(t.Context(), config)
				if err != nil {
					t.Fatal(err)
				}
				defer store.Close()
				current, _ := store.Get(m.ID)
				v := onlyResolution(t, current)
				if v.OpeningActionID != opening.ID || v.ActionID != closing.ID || v.DeliveryID != closing.DeliveryID || v.State != Unknown || v.ExecutionDeadline != closing.Deadline || store.fsm.image.ActionProjectionState.Notifications != nil {
					t.Fatal("restart changed or lost historical closure evidence")
				}
				if got := snapshotAction(t, store.fsm.collections, opening.ID); !reflect.DeepEqual(got, opening) {
					t.Fatal("restart altered the original opening")
				}
				retained, _ := monitorWithActionHistory(t, store, m.ID)
				if len(retained.Actions) != len(m.Actions) || retained.Actions[closing.ID].State != Unknown {
					t.Fatal("restart duplicated or replayed an interrupted action")
				}
				if err := store.Snapshot(); err != nil {
					t.Fatal(err)
				}
			})
		}
	}
}

func TestNotificationProjectionAdministrativeRestoreBeforeFencing(t *testing.T) {
	formats := []int{ActionFormatVersion}
	if externalStorageFormat(45) {
		formats = append(formats, 45)
	}
	for _, format := range formats {
		t.Run(fmt.Sprintf("format%d", format), func(t *testing.T) {
			config := testConfig(t)
			store, err := openReplayStore(t, config)
			if err != nil {
				t.Fatal(err)
			}
			_, m, opening, closing := archivedNotificationFixtureOn(t, store, format, 0, Started)
			if state := store.fsm.image.ActionProjectionState; !state.Ready || state.Notifications != nil || !requiresProjectionMigration(store.fsm.image) {
				t.Fatal("fixture lacks unconverted historical closure evidence")
			}
			persistPausedReviewSnapshot(t, store)
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}
			restored := filepath.Join(t.TempDir(), "restored")
			copyRestoreFixture(t, config.Storage.Directory, restored)
			if err := MarkRestored(restored, time.Now().UTC()); err != nil {
				t.Fatal(err)
			}
			config.Storage.Directory = restored
			store, err = OpenAdministrative(t.Context(), config)
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			current, _ := store.Get(m.ID)
			v := onlyResolution(t, current)
			if v.OpeningActionID != opening.ID || v.ActionID != closing.ID || v.State != Unknown || v.ExecutionDeadline != closing.Deadline || !v.Started || requiresProjectionMigration(store.fsm.image) {
				t.Fatal("restore fencing preceded or invalidated original closure binding")
			}
			if got := snapshotAction(t, store.fsm.collections, opening.ID); !reflect.DeepEqual(got, opening) {
				t.Fatal("administrative restore rewrote the original opening")
			}
			if got := snapshotAction(t, store.fsm.collections, closing.ID); got.State != Unknown || got.Deadline != closing.Deadline || got.Outcome != "explicit_restore_while_started" {
				t.Fatal("administrative restore lost its interrupted outcome hold")
			}
			if err := persistStoreFixture(t, store); err != nil {
				t.Fatal(err)
			}
		})
	}
}
