package dynamo

import (
	"context"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apiserver/pkg/apis/example"
	"k8s.io/apiserver/pkg/storage"
)

// TestUnknownFieldDroppedOnUpdate shows why spec 11.3 needs the format
// check of the storage guard: a server that reads an object with a field
// it does not know, changes it, and writes it back drops the field. The
// store itself cannot keep it. The guard stops the write instead
// (pkg/apiserver TestFormat).
func TestUnknownFieldDroppedOnUpdate(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	value := []byte(`{"kind":"Pod","apiVersion":"example.apiserver.k8s.io/v1",` +
		`"metadata":{"name":"p1","namespace":"ns"},"spec":{"nodeName":"n1","futureField":"x"}}`)
	if _, err := s.commit(ctx, write{sk: "/ns/p1", action: actCreate, value: value}); err != nil {
		t.Fatal(err)
	}
	err := s.GuaranteedUpdate(ctx, "/pods/ns/p1", &example.Pod{}, false, nil,
		func(in runtime.Object, _ storage.ResponseMeta) (runtime.Object, *uint64, error) {
			p := in.(*example.Pod).DeepCopy()
			p.Labels = map[string]string{"touched": "yes"}
			return p, nil, nil
		}, nil)
	if err != nil {
		t.Fatal(err)
	}
	out, err := s.client.GetItem(ctx, &dynamodb.GetItemInput{
		TableName:      aws.String(s.table),
		Key:            map[string]types.AttributeValue{attrPK: str(s.objectPK()), attrSK: str("/ns/p1")},
		ConsistentRead: aws.Bool(true),
	})
	if err != nil {
		t.Fatal(err)
	}
	raw := string(out.Item[attrValue].(*types.AttributeValueMemberB).Value)
	if strings.Contains(raw, "futureField") || !strings.Contains(raw, "touched") {
		t.Fatalf("stored object after the update = %s; want the label and no futureField", raw)
	}
}
