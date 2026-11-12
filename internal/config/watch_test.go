// SPDX-License-Identifier: Apache-2.0

package config

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/go-logr/logr"
	. "github.com/onsi/gomega"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/watch"
	toolscache "k8s.io/client-go/tools/cache"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"

	bootcv1alpha1 "github.com/bootc-dev/bootc-operator/api/v1alpha1"
)

const (
	eventually = 10 * time.Second
	poll       = 20 * time.Millisecond
)

func TestMain(m *testing.M) {
	// The fake API serves a plain list and watch, not the streaming list.
	// The gate is read once, on first use.
	if err := os.Setenv("KUBE_FEATURE_WatchListClient", "false"); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to set feature gate: %v\n", err)
		os.Exit(1)
	}
	os.Exit(m.Run())
}

// fakeAPI is an API server for BootcOperatorConfigs that tests drive: the
// list it serves, and the watch connection that can be dropped.
type fakeAPI struct {
	mu      sync.Mutex
	configs map[string]*bootcv1alpha1.BootcOperatorConfig
	rv      int
	watcher *watch.FakeWatcher
	lists   int
	expire  bool
}

func newFakeAPI(configs ...*bootcv1alpha1.BootcOperatorConfig) *fakeAPI {
	api := &fakeAPI{configs: map[string]*bootcv1alpha1.BootcOperatorConfig{}}
	for _, cfg := range configs {
		api.put(cfg)
	}
	return api
}

func (a *fakeAPI) put(cfg *bootcv1alpha1.BootcOperatorConfig) {
	a.rv++
	cfg = cfg.DeepCopy()
	cfg.ResourceVersion = strconv.Itoa(a.rv)
	a.configs[cfg.Name] = cfg
}

// apply creates or updates cfg and sends the watch event.
func (a *fakeAPI) apply(cfg *bootcv1alpha1.BootcOperatorConfig) {
	a.mu.Lock()
	defer a.mu.Unlock()
	_, existed := a.configs[cfg.Name]
	a.put(cfg)
	if a.watcher != nil {
		if existed {
			a.watcher.Modify(a.configs[cfg.Name].DeepCopy())
		} else {
			a.watcher.Add(a.configs[cfg.Name].DeepCopy())
		}
	}
}

// remove deletes the named config and sends the watch event.
func (a *fakeAPI) remove(name string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	cfg := a.configs[name]
	delete(a.configs, name)
	a.rv++
	if a.watcher != nil && cfg != nil {
		a.watcher.Delete(cfg.DeepCopy())
	}
}

// breakWatch drops the watch connection and makes the API server refuse to
// resume it, as after a long outage, so the informer has to relist. Changes
// made with silently are never sent, so only the relist reveals them.
func (a *fakeAPI) breakWatch(silently func()) {
	a.mu.Lock()
	defer a.mu.Unlock()
	watcher := a.watcher
	a.watcher = nil
	a.expire = true
	if watcher != nil {
		// A watch that ends without a single event counts as a failed
		// attempt, which makes the informer back off for several seconds.
		for _, cfg := range a.configs {
			watcher.Modify(cfg.DeepCopy())
			break
		}
	}
	if silently != nil {
		silently()
	}
	if watcher != nil {
		watcher.Stop()
	}
}

func (a *fakeAPI) watching() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.watcher != nil
}

func (a *fakeAPI) listCount() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.lists
}

// GetInformer returns a running informer, as a controller-runtime cache does.
func (a *fakeAPI) GetInformer(
	ctx context.Context,
	_ client.Object,
	_ ...cache.InformerGetOption,
) (cache.Informer, error) {
	informer := toolscache.NewSharedIndexInformer(&toolscache.ListWatch{
		ListFunc: func(metav1.ListOptions) (runtime.Object, error) {
			a.mu.Lock()
			defer a.mu.Unlock()
			a.lists++
			list := &bootcv1alpha1.BootcOperatorConfigList{
				ListMeta: metav1.ListMeta{ResourceVersion: strconv.Itoa(a.rv)},
			}
			for _, cfg := range a.configs {
				list.Items = append(list.Items, *cfg.DeepCopy())
			}
			return list, nil
		},
		WatchFunc: func(metav1.ListOptions) (watch.Interface, error) {
			a.mu.Lock()
			defer a.mu.Unlock()
			if a.expire {
				a.expire = false
				return nil, apierrors.NewResourceExpired("too old resource version")
			}
			a.watcher = watch.NewFakeWithChanSize(10, false)
			return a.watcher, nil
		},
	}, &bootcv1alpha1.BootcOperatorConfig{}, 0, toolscache.Indexers{})
	go informer.Run(ctx.Done())
	return informer, nil
}

func newConfig(name string, seconds int32) *bootcv1alpha1.BootcOperatorConfig {
	return &bootcv1alpha1.BootcOperatorConfig{
		ObjectMeta: metav1.ObjectMeta{Name: name, UID: types.UID("uid-" + name), Generation: 1},
		Spec: bootcv1alpha1.BootcOperatorConfigSpec{
			Controller: &bootcv1alpha1.OperatorControllerConfig{
				TagResolutionPeriodSeconds: ptr.To(seconds),
			},
		},
	}
}

var testFallback = Settings{
	AllowInsecureRegistry: true,
	TagResolutionInterval: 5 * time.Minute,
	BootcPollInterval:     7 * time.Minute,
}

// runWatcher starts a Watcher on api, seeded with initial, and returns it
// once synced along with a counter of its change notifications.
func runWatcher(
	t *testing.T,
	api *fakeAPI,
	initial *bootcv1alpha1.BootcOperatorConfig,
) (*Watcher, func() int) {
	t.Helper()
	g := NewWithT(t)
	w := NewWatcher(testFallback, initial, api, logr.Discard())
	var mu sync.Mutex
	changes := 0
	w.OnChange(func(_, _ State) {
		mu.Lock()
		defer mu.Unlock()
		changes++
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- w.Start(ctx) }()
	t.Cleanup(func() {
		cancel()
		g.Eventually(done, eventually).Should(Receive(BeNil()))
	})
	g.Eventually(api.listCount, eventually, poll).Should(BeNumerically(">=", 1))
	g.Eventually(api.watching, eventually, poll).Should(BeTrue())
	return w, func() int {
		mu.Lock()
		defer mu.Unlock()
		return changes
	}
}

func interval(w *Watcher) func() time.Duration {
	return func() time.Duration { return w.State().Settings.TagResolutionInterval }
}

func TestWatcherStartsFromLoadedConfiguration(t *testing.T) {
	g := NewWithT(t)
	cfg := newConfig("custom", 30)
	w := NewWatcher(testFallback, cfg, newFakeAPI(cfg), logr.Discard())
	g.Expect(w.State().Settings.TagResolutionInterval).To(Equal(30 * time.Second))
	g.Expect(w.State().Active.Name).To(Equal("custom"))

	w = NewWatcher(testFallback, nil, newFakeAPI(), logr.Discard())
	g.Expect(w.State().Settings).To(Equal(testFallback))
	g.Expect(w.State().Active).To(BeNil())
}

func TestWatcherAppliesCreateUpdateDelete(t *testing.T) {
	g := NewWithT(t)
	api := newFakeAPI()
	w, changes := runWatcher(t, api, nil)
	g.Expect(w.State().Settings).To(Equal(testFallback))
	g.Expect(changes()).To(BeZero(), "the synced fallback is not a change")

	api.apply(newConfig("custom", 30))
	g.Eventually(interval(w), eventually, poll).Should(Equal(30 * time.Second))
	// Unset fields keep the CLI fallback.
	g.Expect(w.State().Settings.AllowInsecureRegistry).To(BeTrue())
	g.Expect(w.State().Settings.BootcPollInterval).To(Equal(7 * time.Minute))
	g.Expect(changes()).To(Equal(1))

	updated := newConfig("custom", 60)
	updated.Generation = 2
	api.apply(updated)
	g.Eventually(interval(w), eventually, poll).Should(Equal(time.Minute))
	g.Expect(changes()).To(Equal(2))

	api.remove("custom")
	g.Eventually(interval(w), eventually, poll).Should(Equal(5 * time.Minute))
	g.Expect(w.State().Settings).To(Equal(testFallback))
	g.Expect(w.State().Active).To(BeNil())
	g.Expect(changes()).To(Equal(3))
}

func TestWatcherNotifiesOnlyOnChange(t *testing.T) {
	g := NewWithT(t)
	cfg := newConfig("custom", 30)
	api := newFakeAPI(cfg)
	w, changes := runWatcher(t, api, cfg)
	g.Expect(changes()).To(BeZero())

	// Same generation: a status or metadata update, not a new configuration.
	same := newConfig("custom", 30)
	same.Labels = map[string]string{"a": "b"}
	api.apply(same)
	g.Consistently(changes, 200*time.Millisecond, poll).Should(BeZero())
	g.Expect(w.State().Settings.TagResolutionInterval).To(Equal(30 * time.Second))
}

func TestWatcherRestoresFallbackWhenConfigurationVanishedBeforeSync(t *testing.T) {
	g := NewWithT(t)
	// Load found it, then it was deleted before the informer listed.
	w, _ := runWatcher(t, newFakeAPI(), newConfig("custom", 30))
	g.Eventually(interval(w), eventually, poll).Should(Equal(5 * time.Minute))
	g.Expect(w.State().Active).To(BeNil())
}

func TestWatcherKeepsSettingsWhileConfigurationsConflict(t *testing.T) {
	g := NewWithT(t)
	api := newFakeAPI()
	w, _ := runWatcher(t, api, nil)

	api.apply(newConfig("a", 30))
	g.Eventually(interval(w), eventually, poll).Should(Equal(30 * time.Second))

	api.apply(newConfig("b", 60))
	g.Eventually(func() []string { return w.State().Conflicts }, eventually, poll).
		Should(Equal([]string{"a", "b"}))
	g.Expect(w.State().Settings.TagResolutionInterval).To(Equal(30*time.Second),
		"neither configuration is picked")

	// Even the one that was active can go: the remaining one then applies.
	api.remove("a")
	g.Eventually(interval(w), eventually, poll).Should(Equal(time.Minute))
	g.Expect(w.State().Conflicts).To(BeEmpty())
	g.Expect(w.State().Active.Name).To(Equal("b"))
}

func TestWatcherConflictAtEndRestoresFallback(t *testing.T) {
	g := NewWithT(t)
	api := newFakeAPI()
	w, _ := runWatcher(t, api, nil)
	api.apply(newConfig("a", 30))
	api.apply(newConfig("b", 60))
	g.Eventually(func() []string { return w.State().Conflicts }, eventually, poll).
		Should(HaveLen(2))
	api.remove("a")
	api.remove("b")
	g.Eventually(func() State { return w.State() }, eventually, poll).
		Should(And(
			HaveField("Settings", Equal(testFallback)),
			HaveField("Conflicts", BeEmpty()),
			HaveField("Active", BeNil()),
		))
}

func TestWatcherKeepsSettingsForInvalidConfiguration(t *testing.T) {
	g := NewWithT(t)
	api := newFakeAPI()
	w, _ := runWatcher(t, api, nil)
	api.apply(newConfig("custom", 30))
	g.Eventually(interval(w), eventually, poll).Should(Equal(30 * time.Second))

	invalid := newConfig("custom", 0)
	invalid.Generation = 2
	api.apply(invalid)
	g.Consistently(interval(w), 200*time.Millisecond, poll).Should(Equal(30 * time.Second))

	valid := newConfig("custom", 45)
	valid.Generation = 3
	api.apply(valid)
	g.Eventually(interval(w), eventually, poll).Should(Equal(45 * time.Second))
}

func TestWatcherSurvivesWatchOutage(t *testing.T) {
	g := NewWithT(t)
	api := newFakeAPI()
	w, _ := runWatcher(t, api, nil)
	api.apply(newConfig("custom", 30))
	g.Eventually(interval(w), eventually, poll).Should(Equal(30 * time.Second))
	lists := api.listCount()

	// The connection drops: the settings stay while it is down.
	api.breakWatch(nil)
	g.Consistently(interval(w), 200*time.Millisecond, poll).Should(Equal(30 * time.Second))
	g.Eventually(api.listCount, eventually, poll).Should(BeNumerically(">", lists))
	g.Eventually(api.watching, eventually, poll).Should(BeTrue())

	// A change made during an outage shows up once it is relisted.
	updated := newConfig("custom", 90)
	updated.Generation = 2
	api.breakWatch(func() { api.put(updated) })
	g.Eventually(interval(w), eventually, poll).Should(Equal(90 * time.Second))
	g.Eventually(api.watching, eventually, poll).Should(BeTrue())

	// So does a deletion.
	api.breakWatch(func() { delete(api.configs, "custom") })
	g.Eventually(interval(w), eventually, poll).Should(Equal(5 * time.Minute))
	g.Expect(w.State().Active).To(BeNil(), fmt.Sprintf("%+v", w.State()))
}

func TestWatcherReportsPreviousAndNewState(t *testing.T) {
	g := NewWithT(t)
	api := newFakeAPI()
	w := NewWatcher(testFallback, nil, api, logr.Discard())
	type change struct{ prev, next State }
	changes := make(chan change, 4)
	w.OnChange(func(prev, next State) { changes <- change{prev, next} })
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- w.Start(ctx) }()
	defer func() {
		cancel()
		g.Eventually(done, eventually).Should(Receive(BeNil()))
	}()
	g.Eventually(api.watching, eventually, poll).Should(BeTrue())

	api.apply(newConfig("custom", 30))

	var got change
	g.Eventually(changes, eventually).Should(Receive(&got))
	g.Expect(got.prev.Settings.TagResolutionInterval).To(Equal(5 * time.Minute))
	g.Expect(got.prev.Active).To(BeNil())
	g.Expect(got.next.Settings.TagResolutionInterval).To(Equal(30 * time.Second))
	g.Expect(got.next.Active.Name).To(Equal("custom"))
}
