package postgres

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"testing"

	storagetesting "k8s.io/apiserver/pkg/storage/testing"
)

// The conformance tests are the tests that the etcd3 store runs. Tests
// that need history, compaction, TTL, or a value transformer are left
// out, because this store does not have them. ListPaging and
// GetListNonRecursive are left out too: they read pages or objects at an
// older resource version, and this store returns 410 Gone for that, as
// spec 2.4 says. list_test.go checks that behavior.
func TestConformance(t *testing.T) {
	noValidation := func(context.Context, *testing.T, string) {}
	run := func(name string, f func(context.Context, *testing.T, *store)) {
		t.Run(name, func(t *testing.T) {
			f(context.Background(), t, newTestStore(t))
		})
	}

	run("Create", func(ctx context.Context, t *testing.T, s *store) {
		storagetesting.RunTestCreate(ctx, t, s, noValidation)
	})
	run("CreateWithKeyExist", func(ctx context.Context, t *testing.T, s *store) {
		storagetesting.RunTestCreateWithKeyExist(ctx, t, s)
	})
	run("Get", func(ctx context.Context, t *testing.T, s *store) {
		storagetesting.RunTestGet(ctx, t, s)
	})

	deletes := map[string]func(context.Context, *testing.T, *store){
		"UnconditionalDelete":  func(c context.Context, t *testing.T, s *store) { storagetesting.RunTestUnconditionalDelete(c, t, s) },
		"ConditionalDelete":    func(c context.Context, t *testing.T, s *store) { storagetesting.RunTestConditionalDelete(c, t, s) },
		"DeleteWithSuggestion": func(c context.Context, t *testing.T, s *store) { storagetesting.RunTestDeleteWithSuggestion(c, t, s) },
		"DeleteWithSuggestionAndConflict": func(c context.Context, t *testing.T, s *store) {
			storagetesting.RunTestDeleteWithSuggestionAndConflict(c, t, s)
		},
		"DeleteWithSuggestionOfDeletedObject": func(c context.Context, t *testing.T, s *store) {
			storagetesting.RunTestDeleteWithSuggestionOfDeletedObject(c, t, s)
		},
		"ValidateDeletionWithSuggestion": func(c context.Context, t *testing.T, s *store) {
			storagetesting.RunTestValidateDeletionWithSuggestion(c, t, s)
		},
		"ValidateDeletionWithOnlySuggestionValid": func(c context.Context, t *testing.T, s *store) {
			storagetesting.RunTestValidateDeletionWithOnlySuggestionValid(c, t, s)
		},
		"DeleteWithConflict": func(c context.Context, t *testing.T, s *store) { storagetesting.RunTestDeleteWithConflict(c, t, s) },
		"PreconditionalDeleteWithSuggestion": func(c context.Context, t *testing.T, s *store) {
			storagetesting.RunTestPreconditionalDeleteWithSuggestion(c, t, s)
		},
		"PreconditionalDeleteWithOnlySuggestionPass": func(c context.Context, t *testing.T, s *store) {
			storagetesting.RunTestPreconditionalDeleteWithOnlySuggestionPass(c, t, s)
		},
	}
	for name, f := range deletes {
		run(name, f)
	}

	run("GuaranteedUpdateWithConflict", func(ctx context.Context, t *testing.T, s *store) {
		storagetesting.RunTestGuaranteedUpdateWithConflict(ctx, t, s)
	})
	run("GuaranteedUpdateWithSuggestionAndConflict", func(ctx context.Context, t *testing.T, s *store) {
		storagetesting.RunTestGuaranteedUpdateWithSuggestionAndConflict(ctx, t, s)
	})
	run("GuaranteedUpdate", func(ctx context.Context, t *testing.T, s *store) {
		storagetesting.RunTestGuaranteedUpdate(ctx, t, noTransformer{s}, noValidation)
	})

	noCalls := func(*testing.T, uint64, uint64) {}
	run("GetListRecursivePrefix", func(ctx context.Context, t *testing.T, s *store) {
		storagetesting.RunTestGetListRecursivePrefix(ctx, t, s)
	})
	run("KeySchema", func(ctx context.Context, t *testing.T, s *store) {
		storagetesting.RunTestKeySchema(ctx, t, s)
	})
	run("ListContinuation", func(ctx context.Context, t *testing.T, s *store) {
		storagetesting.RunTestListContinuation(ctx, t, s, noCalls)
	})
	run("ListPaginationRareObject", func(ctx context.Context, t *testing.T, s *store) {
		storagetesting.RunTestListPaginationRareObject(ctx, t, s, noCalls)
	})
	run("ListContinuationWithFilter", func(ctx context.Context, t *testing.T, s *store) {
		storagetesting.RunTestListContinuationWithFilter(ctx, t, s, noCalls)
	})
	run("NamespaceScopedList", func(ctx context.Context, t *testing.T, s *store) {
		storagetesting.RunTestNamespaceScopedList(ctx, t, s)
	})

	watches := map[string]func(context.Context, *testing.T, *store){
		"Watch":                func(c context.Context, t *testing.T, s *store) { storagetesting.RunTestWatch(c, t, s) },
		"ClusterScopedWatch":   func(c context.Context, t *testing.T, s *store) { storagetesting.RunTestClusterScopedWatch(c, t, s) },
		"NamespaceScopedWatch": func(c context.Context, t *testing.T, s *store) { storagetesting.RunTestNamespaceScopedWatch(c, t, s) },
		"DeleteTriggerWatch":   func(c context.Context, t *testing.T, s *store) { storagetesting.RunTestDeleteTriggerWatch(c, t, s) },
		"WatchFromNonZero":     func(c context.Context, t *testing.T, s *store) { storagetesting.RunTestWatchFromNonZero(c, t, s) },
		"DelayedWatchDelivery": func(c context.Context, t *testing.T, s *store) { storagetesting.RunTestDelayedWatchDelivery(c, t, s) },
		"WatchContextCancel":   func(c context.Context, t *testing.T, s *store) { storagetesting.RunTestWatchContextCancel(c, t, s) },
		"WatcherTimeout":       func(c context.Context, t *testing.T, s *store) { storagetesting.RunTestWatcherTimeout(c, t, s) },
		"WatchDeleteEventObjectHaveLatestRV": func(c context.Context, t *testing.T, s *store) {
			storagetesting.RunTestWatchDeleteEventObjectHaveLatestRV(c, t, s)
		},
		"WatchInitializationSignal": func(c context.Context, t *testing.T, s *store) {
			storagetesting.RunTestWatchInitializationSignal(c, t, s)
		},
		"ProgressNotify": func(c context.Context, t *testing.T, s *store) {
			storagetesting.RunOptionalTestProgressNotify(c, t, s, increaseRV(s))
		},
		"WatchDispatchBookmarkEvents": func(c context.Context, t *testing.T, s *store) {
			storagetesting.RunTestWatchDispatchBookmarkEvents(c, t, s, false)
		},
		"SendInitialEventsBackwardCompatibility": func(c context.Context, t *testing.T, s *store) {
			storagetesting.RunSendInitialEventsBackwardCompatibility(c, t, s)
		},
		"WatchSemantics": func(c context.Context, t *testing.T, s *store) { storagetesting.RunWatchSemantics(c, t, s) },
		"WatchSemanticInitialEventsExtended": func(c context.Context, t *testing.T, s *store) {
			storagetesting.RunWatchSemanticInitialEventsExtended(c, t, s)
		},
		"WatchListMatchSingle": func(c context.Context, t *testing.T, s *store) { storagetesting.RunWatchListMatchSingle(c, t, s) },
	}
	for name, f := range watches {
		run(name, f)
	}
}

// noTransformer satisfies InterfaceWithPrefixTransformer for tests that
// accept a store with no value transformer.
type noTransformer struct{ *store }

func (noTransformer) UpdatePrefixTransformer(storagetesting.PrefixTransformerModifier) func() {
	return func() {}
}

// increaseRV writes an object to a key outside the tests' keys, so the
// resource version moves on.
func increaseRV(s *store) storagetesting.IncreaseRVFunc {
	return func(ctx context.Context, t *testing.T) int64 {
		rv, err := s.commit(ctx, write{key: "/zz-increase-rv/" + randomKey(), action: actCreate, value: []byte("{}")})
		if err != nil {
			t.Fatal(err)
		}
		return int64(rv)
	}
}

// randomKey returns a key part that no other call returns.
func randomKey() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
