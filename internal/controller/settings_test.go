// SPDX-License-Identifier: Apache-2.0

package controller

import (
	"context"
	"sync"
	"testing"
	"time"

	. "github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/bootc-dev/bootc-operator/internal/config"
	testutil "github.com/bootc-dev/bootc-operator/test/util"
)

// insecureTrackingResolver records the insecure setting it was given.
type insecureTrackingResolver struct {
	staticTagResolver
	mu       sync.Mutex
	insecure bool
	calls    int
}

func (r *insecureTrackingResolver) SetAllowInsecure(allowInsecure bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.insecure = allowInsecure
	r.calls++
}

func newSettingsReconciler(resolver TagResolver) *BootcNodePoolReconciler {
	return &BootcNodePoolReconciler{
		TagResolver:           resolver,
		TagResolutionInterval: 5 * time.Minute,
		settingsCh:            make(chan event.GenericEvent, 1),
	}
}

func TestApplySettings(t *testing.T) {
	base := config.Settings{AllowInsecureRegistry: false, TagResolutionInterval: 5 * time.Minute}
	for _, tt := range []struct {
		name         string
		next         config.Settings
		wantEvent    bool
		wantInterval time.Duration
		wantInsecure bool
		wantCalls    int
	}{
		{
			name:         "unrelated settings change nothing",
			next:         config.Settings{TagResolutionInterval: 5 * time.Minute, BootcPollInterval: time.Second},
			wantInterval: 5 * time.Minute,
		},
		{
			name:         "interval",
			next:         config.Settings{TagResolutionInterval: time.Minute},
			wantEvent:    true,
			wantInterval: time.Minute,
			wantCalls:    1,
		},
		{
			name:         "insecure registries",
			next:         config.Settings{AllowInsecureRegistry: true, TagResolutionInterval: 5 * time.Minute},
			wantEvent:    true,
			wantInterval: 5 * time.Minute,
			wantInsecure: true,
			wantCalls:    1,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			g := NewWithT(t)
			resolver := &insecureTrackingResolver{}
			r := newSettingsReconciler(resolver)

			r.ApplySettings(base, tt.next)

			g.Expect(r.tagResolutionInterval()).To(Equal(tt.wantInterval))
			g.Expect(resolver.insecure).To(Equal(tt.wantInsecure))
			g.Expect(resolver.calls).To(Equal(tt.wantCalls))
			if tt.wantEvent {
				g.Expect(r.settingsCh).To(Receive())
			} else {
				g.Expect(r.settingsCh).NotTo(Receive())
			}
		})
	}
}

func TestApplySettingsCoalescesEventsAndToleratesPlainResolvers(t *testing.T) {
	g := NewWithT(t)
	r := newSettingsReconciler(staticTagResolver{})
	prev := config.Settings{TagResolutionInterval: time.Minute}

	// Neither blocks when nobody has consumed the first event, nor needs a
	// resolver that can change its insecure setting.
	r.ApplySettings(prev, config.Settings{TagResolutionInterval: 2 * time.Minute})
	r.ApplySettings(prev, config.Settings{TagResolutionInterval: 3 * time.Minute})

	g.Expect(r.settingsCh).To(HaveLen(1))
	g.Expect(r.tagResolutionInterval()).To(Equal(3 * time.Minute))
}

func TestApplySettingsBeforeSetup(t *testing.T) {
	g := NewWithT(t)
	r := &BootcNodePoolReconciler{TagResolutionInterval: time.Minute}
	g.Expect(func() {
		r.ApplySettings(
			config.Settings{TagResolutionInterval: time.Minute},
			config.Settings{TagResolutionInterval: time.Hour},
		)
	}).NotTo(Panic())
	g.Expect(r.tagResolutionInterval()).To(Equal(time.Hour))
}

func TestApplySettingsConcurrentWithReconciles(t *testing.T) {
	g := NewWithT(t)
	r := newSettingsReconciler(&insecureTrackingResolver{})
	pool := testutil.NewPool("racy", testutil.ImageTaggedRef, testutil.WithWorkerSelector())

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := range 200 {
			r.ApplySettings(
				config.Settings{TagResolutionInterval: time.Duration(i) * time.Second},
				config.Settings{TagResolutionInterval: time.Duration(i+1) * time.Second},
			)
			select {
			case <-r.settingsCh:
			default:
			}
		}
	}()
	go func() {
		defer wg.Done()
		for range 200 {
			p := pool.DeepCopy()
			_, _, err := r.resolveTargetDigest(context.Background(), p, nil)
			g.Expect(err).NotTo(HaveOccurred())
		}
	}()
	wg.Wait()
}

func TestMapSettingsToPoolRequestsOnlyTagPools(t *testing.T) {
	g := NewWithT(t)
	tagPool := testutil.NewPool("tag-pool", testutil.ImageTaggedRef, testutil.WithWorkerSelector())
	otherTagPool := testutil.NewPool(
		"other-tag-pool",
		testutil.ImageTaggedRef,
		testutil.WithWorkerSelector(),
	)
	digestPool := testutil.NewPool(
		"digest-pool",
		testutil.ImageDigestRefA,
		testutil.WithWorkerSelector(),
	)
	invalidPool := testutil.NewPool("invalid-pool", "not a ref", testutil.WithWorkerSelector())
	r := newSettingsReconciler(staticTagResolver{})
	r.Client = fake.NewClientBuilder().
		WithScheme(scheme.Scheme).
		WithObjects(tagPool, otherTagPool, digestPool, invalidPool).
		Build()

	requests := r.mapSettingsToPoolRequests(context.Background(), nil)

	g.Expect(requests).To(ConsistOf(
		reconcile.Request{NamespacedName: types.NamespacedName{Name: "tag-pool"}},
		reconcile.Request{NamespacedName: types.NamespacedName{Name: "other-tag-pool"}},
	))
	g.Expect(r.takeForceResolve("tag-pool")).To(BeTrue())
	g.Expect(r.takeForceResolve("tag-pool")).To(BeFalse(), "the mark is used up by one reconcile")
	g.Expect(r.takeForceResolve("other-tag-pool")).To(BeTrue())
	g.Expect(r.takeForceResolve("digest-pool")).To(BeFalse())
	g.Expect(r.takeForceResolve("invalid-pool")).To(BeFalse())
}

func TestSettingsChangeOverridesTheOldSchedule(t *testing.T) {
	g := NewWithT(t)
	pool := testutil.NewPool("scheduled", testutil.ImageTaggedRef, testutil.WithWorkerSelector())
	pool.Status.TargetDigest = testDigestA
	// Scheduled by the old interval, long from now.
	later := metav1.NewTime(time.Now().Add(time.Hour))
	pool.Status.NextTagResolutionTime = &later
	r := newSettingsReconciler(staticTagResolver{digest: testDigestB})
	r.Client = fake.NewClientBuilder().
		WithScheme(scheme.Scheme).
		WithObjects(pool.DeepCopy()).
		Build()

	// Without a settings change, the schedule holds.
	deferred := pool.DeepCopy()
	result, changed, err := r.resolveTargetDigest(context.Background(), deferred, nil)
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(changed).To(BeFalse())
	g.Expect(deferred.Status.TargetDigest).To(Equal(testDigestA))
	g.Expect(result.RequeueAfter).To(BeNumerically(">", 50*time.Minute))

	// With one, it resolves now and schedules by the new interval.
	r.ApplySettings(
		config.Settings{TagResolutionInterval: 5 * time.Minute},
		config.Settings{TagResolutionInterval: 30 * time.Second},
	)
	r.mapSettingsToPoolRequests(context.Background(), nil)
	resolved := pool.DeepCopy()
	result, changed, err = r.resolveTargetDigest(context.Background(), resolved, nil)
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(changed).To(BeTrue())
	g.Expect(resolved.Status.TargetDigest).To(Equal(testDigestB))
	g.Expect(result.RequeueAfter).To(Equal(30 * time.Second))
	g.Expect(resolved.Status.NextTagResolutionTime.Time).
		To(BeTemporally("~", time.Now().Add(30*time.Second), 5*time.Second))

	// The mark is used up: the next reconcile follows the new schedule.
	again := resolved.DeepCopy()
	again.Status.TargetDigest = testDigestA
	_, changed, err = r.resolveTargetDigest(context.Background(), again, nil)
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(changed).To(BeFalse())
	g.Expect(again.Status.TargetDigest).To(Equal(testDigestA))
}

func TestSettingsChangeLeavesDigestPoolsAlone(t *testing.T) {
	g := NewWithT(t)
	pool := testutil.NewPool("digest", testutil.ImageDigestRefA, testutil.WithWorkerSelector())
	r := newSettingsReconciler(staticTagResolver{digest: testDigestB})
	r.Client = fake.NewClientBuilder().WithScheme(scheme.Scheme).WithObjects(pool).Build()

	g.Expect(r.mapSettingsToPoolRequests(context.Background(), nil)).To(BeEmpty())
}
