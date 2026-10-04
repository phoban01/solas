package dynamo

import (
	"context"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

//= spec/solas.md#12-1-seal
//# A write that fails because the store is sealed MUST return
//# `503 ServiceUnavailable`.

// errSealed is the error of a write to a sealed store.
func errSealed(resource string) error {
	return apierrors.NewServiceUnavailable(fmt.Sprintf("store is sealed: %s moved to another store", resource))
}

// IsSealed reports whether err is the error of a write to a sealed store.
func IsSealed(err error) bool {
	return apierrors.IsServiceUnavailable(err) && strings.Contains(err.Error(), "store is sealed")
}

//= spec/solas.md#12-1-seal
//# The DynamoDB store seals with the attribute `sealed` on the counter item
//# of each resource.

//= spec/solas.md#12-1-seal
//# A seal MUST NOT change any object.

// Seal seals each resource: a write to it then fails, spec 12.1. Reads
// keep working. prefix is the storage prefix of the API server, and each
// resource prefix has the form /<group>/<resource>.
func Seal(ctx context.Context, cfg Config, prefix string, resourcePrefixes []string) error {
	return setSealed(ctx, cfg, prefix, resourcePrefixes, true)
}

// Unseal removes the seal of each resource.
func Unseal(ctx context.Context, cfg Config, prefix string, resourcePrefixes []string) error {
	return setSealed(ctx, cfg, prefix, resourcePrefixes, false)
}

func setSealed(ctx context.Context, cfg Config, prefix string, resourcePrefixes []string, sealed bool) error {
	for _, rp := range resourcePrefixes {
		s, err := newStore(cfg, nil, nil, nil, prefix, rp, schemaFor(rp))
		if err != nil {
			return err
		}
		//= spec/solas.md#12-1-seal
		//# To seal a resource that has no counter item, the tool MUST create the
		//# counter item with the attribute `sealed`.
		//
		// readCounter creates a missing counter item. The update below then
		// sets the attribute before any write can use the new counter for an
		// object.
		if _, _, err := s.readCounterSealed(ctx); err != nil {
			return err
		}
		pk, sk := s.counterKey()
		update := &types.Update{
			TableName:                aws.String(s.table),
			Key:                      map[string]types.AttributeValue{attrPK: str(pk), attrSK: str(sk)},
			ConditionExpression:      aws.String("attribute_exists(pk)"),
			ExpressionAttributeNames: map[string]string{"#s": attrSealed},
		}
		if sealed {
			update.UpdateExpression = aws.String("SET #s = :t")
			update.ExpressionAttributeValues = map[string]types.AttributeValue{":t": &types.AttributeValueMemberBOOL{Value: true}}
		} else {
			//= spec/solas.md#12-1-seal
			//# An unseal removes the attribute `sealed`.
			update.UpdateExpression = aws.String("REMOVE #s")
		}
		if _, err := s.client.TransactWriteItems(ctx, &dynamodb.TransactWriteItemsInput{
			ClientRequestToken: aws.String(newToken()),
			TransactItems:      []types.TransactWriteItem{{Update: update}},
		}); err != nil {
			return fmt.Errorf("seal %s: %w", s.resource, err)
		}
	}
	return nil
}

// IsStoreSealed reports whether any of the resources is sealed.
func IsStoreSealed(ctx context.Context, cfg Config, prefix string, resourcePrefixes []string) (bool, error) {
	for _, rp := range resourcePrefixes {
		s, err := newStore(cfg, nil, nil, nil, prefix, rp, schemaFor(rp))
		if err != nil {
			return false, err
		}
		_, sealed, err := s.readCounterSealed(ctx)
		if err != nil || sealed {
			return sealed, err
		}
	}
	return false, nil
}

// readCounterSealed returns the last issued resource version and whether
// the resource is sealed. It creates the counter when it does not exist.
func (s *store) readCounterSealed(ctx context.Context) (uint64, bool, error) {
	for {
		pk, sk := s.counterKey()
		out, err := s.client.GetItem(ctx, &dynamodb.GetItemInput{
			TableName: aws.String(s.table),
			Key:       map[string]types.AttributeValue{attrPK: str(pk), attrSK: str(sk)},
			//= spec/solas.md#2-4-reads
			//# Every read of a counter item MUST be a strongly consistent read.
			ConsistentRead: aws.Bool(true),
		})
		if err != nil {
			return 0, false, fmt.Errorf("read counter: %w", err)
		}
		if out.Item != nil {
			n, err := numAttr(out.Item, attrN)
			_, sealed := out.Item[attrSealed]
			return n, sealed, err
		}
		if err := s.initCounter(ctx); err != nil {
			return 0, false, err
		}
	}
}

// schemaFor is only for error messages of a store that Seal builds.
func schemaFor(resourcePrefix string) (gr schema.GroupResource) {
	parts := strings.Split(strings.Trim(resourcePrefix, "/"), "/")
	if len(parts) == 2 {
		gr.Group, gr.Resource = parts[0], parts[1]
	}
	return gr
}

//= spec/solas.md#13-3-resource-versions
//# On the DynamoDB store, the restore tool MUST raise each counter item to
//# at least `epoch * 2^32`.

// RaiseCounters raises the counter item of each resource to at least min,
// so every later write gets a resource version above min, spec 13.3. A
// watch from an older version then finds a gap and fails with 410 Gone.
func RaiseCounters(ctx context.Context, cfg Config, prefix string, resourcePrefixes []string, min uint64) error {
	for _, rp := range resourcePrefixes {
		s, err := newStore(cfg, nil, nil, nil, prefix, rp, schemaFor(rp))
		if err != nil {
			return err
		}
		n, _, err := s.readCounterSealed(ctx)
		if err != nil {
			return err
		}
		if n >= min {
			continue
		}
		pk, sk := s.counterKey()
		_, err = s.client.TransactWriteItems(ctx, &dynamodb.TransactWriteItemsInput{
			ClientRequestToken: aws.String(newToken()),
			TransactItems: []types.TransactWriteItem{{Update: &types.Update{
				TableName:                 aws.String(s.table),
				Key:                       map[string]types.AttributeValue{attrPK: str(pk), attrSK: str(sk)},
				UpdateExpression:          aws.String("SET #n = :min"),
				ConditionExpression:       aws.String("#n < :min"),
				ExpressionAttributeNames:  map[string]string{"#n": attrN},
				ExpressionAttributeValues: map[string]types.AttributeValue{":min": num(min)},
			}}},
		})
		if err != nil {
			return fmt.Errorf("raise the counter of %s: %w", s.resource, err)
		}
	}
	return nil
}
