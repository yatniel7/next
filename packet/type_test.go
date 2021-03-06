package packet

import (
	"testing"

	"github.com/chzyer/test"
)

func TestType(t *testing.T) {
	defer test.New(t)

	var pt Type
	test.True(pt.IsInvalid())
	test.Nil(pt.Marshal([]byte{1}))
	test.Equal(pt, AUTH)
	test.False(pt.IsInvalid())
	test.Equal(pt.Bytes(), []byte{1})
}
