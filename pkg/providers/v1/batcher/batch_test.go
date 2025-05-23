/*
Copyright 2024 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package batcher_test

import (
	"context"
	"fmt"
	"github.com/Pallinder/go-randomdata"
	aws "k8s.io/cloud-provider-aws/pkg/providers/v1"
	"k8s.io/cloud-provider-aws/pkg/providers/v1/batcher"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/samber/lo"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var fakeEC2API aws.FakeEC2
var ctx context.Context
var sequentialNumber = 0
var sequentialNumberLock = new(sync.Mutex)

func TestAWS(t *testing.T) {
	ctx = context.TODO()
	RegisterFailHandler(Fail)
	RunSpecs(t, "Batcher")
}

var _ = BeforeSuite(func() {
	fakeEC2API = &aws.FakeEC2Impl{}
})

var _ = Describe("Batcher", func() {
	var cancelCtx context.Context
	var cancel context.CancelFunc
	var fakeBatcher *FakeBatcher

	BeforeEach(func() {
		cancelCtx, cancel = context.WithCancel(ctx)
	})
	AfterEach(func() {
		// Cancel the context to make sure that we properly clean-up
		cancel()
	})
	Context("Concurrency", func() {
		It("should limit the number of threads that run concurrently from the batcher", func() {
			// This batcher will get canceled at the end of the test run
			fakeBatcher = NewFakeBatcher(cancelCtx, time.Minute, 100)

			// Generate 300 items that add to the batcher
			for i := 0; i < 300; i++ {
				go func() {
					fakeBatcher.batcher.Add(cancelCtx, lo.ToPtr(randomName()))
				}()
			}

			// Check that we get to 100 threads, and we stay at 100 threads
			Eventually(fakeBatcher.activeBatches.Load).Should(BeNumerically("==", 100))
			Consistently(fakeBatcher.activeBatches.Load, time.Second*10).Should(BeNumerically("==", 100))
		})
		It("should process 300 items in parallel to get quicker batching", func() {
			// This batcher will get canceled at the end of the test run
			fakeBatcher = NewFakeBatcher(cancelCtx, time.Second, 300)

			// Generate 300 items that add to the batcher
			for i := 0; i < 300; i++ {
				go func() {
					fakeBatcher.batcher.Add(cancelCtx, lo.ToPtr(randomName()))
				}()
			}

			Eventually(fakeBatcher.activeBatches.Load).Should(BeNumerically("==", 300))
			Eventually(fakeBatcher.completedBatches.Load, time.Second*3).Should(BeNumerically("==", 300))
		})
	})
})

// FakeBatcher is a batcher with a mocked request that takes a long time to execute that also ref-counts the number
// of active requests that are running at a given time
type FakeBatcher struct {
	activeBatches    *atomic.Int64
	completedBatches *atomic.Int64
	batcher          *batcher.Batcher[string, string]
}

func NewFakeBatcher(ctx context.Context, requestLength time.Duration, maxRequestWorkers int) *FakeBatcher {
	activeBatches := &atomic.Int64{}
	completedBatches := &atomic.Int64{}
	options := batcher.Options[string, string]{
		Name:              "fake",
		IdleTimeout:       100 * time.Millisecond,
		MaxTimeout:        1 * time.Second,
		MaxRequestWorkers: maxRequestWorkers,
		RequestHasher:     batcher.DefaultHasher[string],
		BatchExecutor: func(ctx context.Context, items []*string) []batcher.Result[string] {
			// Keep a ref count of the number of batches that we are currently running
			activeBatches.Add(1)
			defer activeBatches.Add(-1)
			defer completedBatches.Add(1)

			// Wait for an arbitrary request length while running this call
			select {
			case <-ctx.Done():
			case <-time.After(requestLength):
			}

			// Return back request responses
			return lo.Map(items, func(i *string, _ int) batcher.Result[string] {
				return batcher.Result[string]{
					Output: lo.ToPtr[string](""),
					Err:    nil,
				}
			})
		},
	}
	return &FakeBatcher{
		activeBatches:    activeBatches,
		completedBatches: completedBatches,
		batcher:          batcher.NewBatcher(ctx, options),
	}
}

func randomName() string {
	sequentialNumberLock.Lock()
	defer sequentialNumberLock.Unlock()
	sequentialNumber++
	return strings.ToLower(fmt.Sprintf("%s-%d-%s", randomdata.SillyName(), sequentialNumber, randomdata.Alphanumeric(10)))
}
