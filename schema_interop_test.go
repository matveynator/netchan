package netchan

import (
	"fmt"
	"github.com/matveynator/netchan/v2/internal/interop"
	"reflect"
	"testing"
)

func TestSchemaStableAcrossCompilers(t *testing.T) {
	typeOfTask := reflect.TypeOf(interop.Task{})
	writer := wireWriter{}
	writeNetworkTypeSchema(&writer, typeOfTask, make(map[reflect.Type]uint32))
	const expected = "010000000119000000326769746875622e636f6d2f6d61747665796e61746f722f6e65746368616e2f76322f696e7465726e616c2f696e7465726f70000000045461736b0000000003000000044e616d6500000000000100000002180000000000000006737472696e6700000000055265706c7900000000000100000003120000000000000000000200000000020000000453746f7000000000000100000004120000000000000000000101000000051900000000000000000000000000"
	if actual := fmt.Sprintf("%x", writer.bytes); actual != expected {
		t.Fatalf("wire schema changed: %s", actual)
	}
}
