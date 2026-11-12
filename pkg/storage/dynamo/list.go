package dynamo

import (
	"context"
	"fmt"
	"reflect"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/conversion"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apiserver/pkg/storage"

	storemetrics "github.com/phoban01/solas/pkg/storage/metrics"
)

// maxListRetries bounds the retries when writes land during a list.
const maxListRetries = 20

func (s *store) GetList(ctx context.Context, key string, opts storage.ListOptions, listObj runtime.Object) error {
	keyPrefix, err := storage.PrepareKey(s.resourcePrefix, key, opts.Recursive)
	if err != nil {
		return err
	}
	skPrefix, err := s.sortKey(key, opts.Recursive)
	if err != nil {
		return err
	}
	listPtr, err := meta.GetItemsPtr(listObj)
	if err != nil {
		return err
	}
	v, err := conversion.EnforcePtr(listPtr)
	if err != nil || v.Kind() != reflect.Slice {
		return fmt.Errorf("need ptr to slice: %v", err)
	}
	newItem := newItemFunc(listObj, v)

	withRev, continueKey, err := storage.ValidateListOptions(keyPrefix, s.versioner, opts)
	if err != nil {
		return err
	}
	startSK := skPrefix
	if continueKey != "" {
		if startSK, err = s.sortKey(continueKey, false); err != nil {
			return apierrors.NewBadRequest(fmt.Sprintf("invalid continue token: %v", err))
		}
	}

	for attempt := 0; ; attempt++ {
		//= spec/solas.md#2-4-reads
		//# A list MUST read the counter before it queries the object items.
		before, err := s.readCounter(ctx)
		if err != nil {
			return err
		}
		if withRev > 0 {
			if err := s.checkExactVersion(uint64(withRev), before, continueKey != ""); err != nil {
				return err
			}
		}

		var items []map[string]types.AttributeValue
		if opts.Recursive {
			items, err = s.queryObjects(ctx, skPrefix, startSK)
		} else {
			var item map[string]types.AttributeValue
			if item, err = s.readObject(ctx, skPrefix); item != nil {
				items = append(items, item)
			}
		}
		if err != nil {
			return err
		}

		//= spec/solas.md#2-4-reads
		//# A list MUST read the counter again after the query.
		after, err := s.readCounter(ctx)
		if err != nil {
			return err
		}
		if before != after {
			storemetrics.Conflicts.WithLabelValues(s.groupResource.String(), storemetrics.CauseList).Inc()
			//= spec/solas.md#2-4-reads
			//# If the two counter values differ, the list MUST retry.
			if attempt >= maxListRetries {
				return apierrors.NewTooManyRequests("writes did not stop long enough for a consistent list", 1)
			}
			if err := sleepJitter(ctx, attempt); err != nil {
				return err
			}
			continue
		}

		//= spec/solas.md#2-4-reads
		//# If the two counter values are equal, the list MUST return that value as
		//# its resource version.
		rev := before
		if withRev == 0 {
			if err := s.validateMinimumResourceVersion(opts.ResourceVersion, rev); err != nil {
				return err
			}
		}
		return s.fillList(listObj, v, newItem, items, keyPrefix, rev, opts)
	}
}

// checkExactVersion checks a list at an exact version. The store keeps only
// the latest state, so it serves only the current version.
func (s *store) checkExactVersion(want, current uint64, isContinue bool) error {
	if want > current {
		return storage.NewTooLargeResourceVersionError(want, current, 0)
	}
	if want == current {
		return nil
	}
	//= spec/solas.md#2-4-reads
	//# A request for a list at an older, exact resource version MUST be served
	//# by the watch cache, or it MUST fail with `410 Gone`.
	if isContinue {
		return apierrors.NewResourceExpired("The provided continue parameter is too old " +
			"to display a consistent list result. You can start a new list without " +
			"the continue parameter.")
	}
	return apierrors.NewResourceExpired(fmt.Sprintf(
		"resource version %d is older than the current version %d, and the store keeps no history", want, current))
}

// queryObjects returns the object items whose sort key has the prefix and is
// not less than start, in sort key order.
func (s *store) queryObjects(ctx context.Context, prefix, start string) ([]map[string]types.AttributeValue, error) {
	cond := "pk = :pk"
	values := map[string]types.AttributeValue{":pk": str(s.objectPK())}
	if start != "" {
		cond += " AND sk >= :start"
		values[":start"] = str(start)
	}
	var items []map[string]types.AttributeValue
	var from map[string]types.AttributeValue
	for {
		out, err := s.client.Query(ctx, &dynamodb.QueryInput{
			TableName:                 aws.String(s.table),
			KeyConditionExpression:    aws.String(cond),
			ExpressionAttributeValues: values,
			ConsistentRead:            aws.Bool(true),
			ExclusiveStartKey:         from,
		})
		if err != nil {
			return nil, fmt.Errorf("query objects: %w", err)
		}
		for _, item := range out.Items {
			if !strings.HasPrefix(strAttr(item, attrSK), prefix) {
				// Items are in sort key order, so the prefix range has ended.
				return items, nil
			}
			items = append(items, item)
		}
		if out.LastEvaluatedKey == nil {
			return items, nil
		}
		from = out.LastEvaluatedKey
	}
}

// fillList decodes the items, applies the predicate and the limit, and sets
// the list metadata.
func (s *store) fillList(listObj runtime.Object, v reflect.Value, newItem func() runtime.Object,
	items []map[string]types.AttributeValue, keyPrefix string, rev uint64, opts storage.ListOptions) error {
	pred := opts.Predicate
	paging := pred.Limit > 0
	var lastKey string
	var hasMore bool
	for _, item := range items {
		if paging && int64(v.Len()) >= pred.Limit {
			hasMore = true
			break
		}
		lastKey = s.storageKey(strAttr(item, attrSK))
		rv, err := numAttr(item, attrRV)
		if err != nil {
			return err
		}
		obj := newItem()
		if err := s.decode(binAttr(item, attrValue), obj, rv); err != nil {
			return err
		}
		matched, err := pred.Matches(obj)
		if err != nil {
			return err
		}
		if matched {
			v.Set(reflect.Append(v, reflect.ValueOf(obj).Elem()))
		}
	}

	continueValue, remaining, err := storage.PrepareContinueToken(lastKey, keyPrefix, int64(rev), int64(len(items)), hasMore, opts)
	if err != nil {
		return err
	}
	if v.IsNil() {
		v.Set(reflect.MakeSlice(v.Type(), 0, 0))
	}
	return s.versioner.UpdateList(listObj, rev, continueValue, remaining)
}

func newItemFunc(listObj runtime.Object, v reflect.Value) func() runtime.Object {
	if list, ok := listObj.(*unstructured.UnstructuredList); ok {
		if apiVersion := list.GetAPIVersion(); apiVersion != "" {
			return func() runtime.Object {
				return &unstructured.Unstructured{Object: map[string]any{"apiVersion": apiVersion}}
			}
		}
	}
	elem := v.Type().Elem()
	return func() runtime.Object {
		return reflect.New(elem).Interface().(runtime.Object)
	}
}
